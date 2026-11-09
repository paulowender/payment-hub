package hub

import (
	"bytes"
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"io"
	"net"
	"net/http"
	"net/netip"
	"net/url"
	"strconv"
	"time"

	"github.com/jackc/pgx/v5"
)

// Secrets and endpoints are snapshotted with the event: changing config cannot reroute queued data.
func enqueueNotifications(ctx context.Context, tx pgx.Tx, tenant string, eventID int64, kind string, data any) error {
	_, err := tx.Exec(ctx, `INSERT INTO notification_deliveries(tenant_id,event_id,endpoint,encrypted_secret,payload)
 SELECT c.tenant_id,e.id,c.url,c.encrypted_secret,convert_to(jsonb_build_object('id',e.id,'tenant_id',e.tenant_id,'type',e.type,'created_at',e.created_at,'version',1,'data',e.data)::text,'UTF8')
 FROM events e JOIN notification_configs c ON c.tenant_id=e.tenant_id
 WHERE e.tenant_id=$1 AND e.id=$2 AND c.enabled ON CONFLICT DO NOTHING`, tenant, eventID)
	return err
}

func notificationURL(raw string) error {
	u, e := url.Parse(raw)
	if e != nil || len(raw) > 2048 || u.Scheme != "https" || u.Hostname() == "" || u.User != nil || u.Fragment != "" {
		return errInvalid
	}
	if p := u.Port(); p != "" && p != "443" {
		return errInvalid
	}
	if ip, e := netip.ParseAddr(u.Hostname()); e == nil && !notificationPublicIP(ip) {
		return errInvalid
	}
	return nil
}
func notificationPublicIP(ip netip.Addr) bool {
	ip = ip.Unmap()
	if ip.Is6() && !netip.MustParsePrefix("2000::/3").Contains(ip) {
		return false
	}
	if !ip.IsGlobalUnicast() || ip.IsPrivate() || ip.IsLoopback() || ip.IsLinkLocalUnicast() {
		return false
	}
	for _, cidr := range []string{"0.0.0.0/8", "100.64.0.0/10", "192.0.0.0/24", "192.0.2.0/24", "192.88.99.0/24", "198.18.0.0/15", "198.51.100.0/24", "203.0.113.0/24", "240.0.0.0/4", "2001::/23", "2001:db8::/32", "2002::/16", "3fff::/20", "64:ff9b::/96", "64:ff9b:1::/48"} {
		if netip.MustParsePrefix(cidr).Contains(ip) {
			return false
		}
	}
	return true
}
func notificationHTTPClient() *http.Client {
	transport := &http.Transport{TLSHandshakeTimeout: 5 * time.Second, ResponseHeaderTimeout: 5 * time.Second, DisableKeepAlives: true}
	transport.DialContext = func(ctx context.Context, network, address string) (net.Conn, error) {
		host, port, err := net.SplitHostPort(address)
		if err != nil {
			return nil, err
		}
		ips, err := net.DefaultResolver.LookupNetIP(ctx, "ip", host)
		if err != nil {
			return nil, err
		}
		if len(ips) == 0 {
			return nil, errInvalid
		}
		for _, ip := range ips {
			if !notificationPublicIP(ip) {
				return nil, errInvalid
			}
		}
		// Dial the validated address itself, preventing a second DNS lookup / rebinding.
		return (&net.Dialer{Timeout: 5 * time.Second}).DialContext(ctx, network, net.JoinHostPort(ips[0].String(), port))
	}
	return &http.Client{Transport: transport, Timeout: 5 * time.Second, CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}
}

type NotificationDispatcher struct {
	Store  *Store
	Vault  *Vault
	Client *http.Client
}

func (d *NotificationDispatcher) DispatchNotifications(ctx context.Context) error {
	// An interrupted final attempt must eventually become visible as a dead letter.
	if _, err := d.Store.DB.Exec(ctx, `UPDATE notification_deliveries SET status='dead',lease_until=NULL,lease_token=NULL,updated_at=now() WHERE status IN ('pending','failed') AND attempts>=8 AND (lease_until IS NULL OR lease_until<now())`); err != nil {
		return err
	}
	client := d.Client
	if client == nil {
		client = notificationHTTPClient()
	}
	for i := 0; i < 10; i++ {
		var tenant, endpoint, lease string
		var id int64
		var attempt int
		var encrypted, body []byte
		lease = token()
		err := d.Store.DB.QueryRow(ctx, `WITH job AS (SELECT tenant_id,event_id FROM notification_deliveries WHERE status IN ('pending','failed') AND attempts<8 AND next_at<=now() AND (lease_until IS NULL OR lease_until<now()) ORDER BY next_at LIMIT 1 FOR UPDATE SKIP LOCKED)
 UPDATE notification_deliveries n SET lease_until=now()+interval '30 seconds',lease_token=$1,attempts=attempts+1 FROM job WHERE n.tenant_id=job.tenant_id AND n.event_id=job.event_id RETURNING n.tenant_id,n.event_id,n.endpoint,n.encrypted_secret,n.payload,n.attempts`, lease).Scan(&tenant, &id, &endpoint, &encrypted, &body, &attempt)
		if errors.Is(err, pgx.ErrNoRows) {
			return nil
		}
		if err != nil {
			return err
		}
		status, code, message := "failed", 0, "delivery_unreachable"
		secret, e := d.Vault.open(tenant+":outbound", encrypted)
		if e == nil {
			e = notificationURL(endpoint)
		}
		if e == nil {
			requestCtx, cancel := context.WithTimeout(ctx, 5*time.Second)
			req, _ := http.NewRequestWithContext(requestCtx, http.MethodPost, endpoint, bytes.NewReader(body))
			timestamp := strconv.FormatInt(time.Now().Unix(), 10)
			mac := hmac.New(sha256.New, secret)
			mac.Write([]byte(timestamp + "."))
			mac.Write(body)
			req.Header.Set("Content-Type", "application/json")
			req.Header.Set("X-PaymentHub-Event-ID", strconv.FormatInt(id, 10))
			req.Header.Set("X-PaymentHub-Timestamp", timestamp)
			req.Header.Set("X-PaymentHub-Signature", "sha256="+hex.EncodeToString(mac.Sum(nil)))
			res, sendErr := client.Do(req)
			if sendErr == nil {
				code = res.StatusCode
				io.Copy(io.Discard, io.LimitReader(res.Body, 4096))
				res.Body.Close()
				if code >= 200 && code < 300 {
					status = "delivered"
					message = ""
				} else {
					message = "endpoint_rejected"
				}
			}
			cancel()
		}
		if status != "delivered" && attempt >= 8 {
			status = "dead"
		}
		_, err = d.Store.DB.Exec(ctx, `UPDATE notification_deliveries SET status=$4,status_code=$5,last_error=$6,next_at=now()+($7 * interval '1 second'),lease_until=NULL,lease_token=NULL,updated_at=now() WHERE tenant_id=$1 AND event_id=$2 AND lease_token=$3`, tenant, id, lease, status, code, message, 30*(1<<uint(attempt-1)))
		if err != nil {
			return err
		}
	}
	return nil
}

// Called only after the console authenticates and validates access to tenant.
// parts is the path after /console/api/projects/{tenant}/notifications/.
func (a *API) notificationAPI(w http.ResponseWriter, r *http.Request, tenant string, parts []string) {
	if a.Vault == nil {
		fail(w, errNotFound)
		return
	}
	ctx := r.Context()
	if len(parts) == 1 && parts[0] == "config" && r.Method == http.MethodGet {
		var endpoint string
		var enabled bool
		err := a.Store.DB.QueryRow(ctx, "SELECT url,enabled FROM notification_configs WHERE tenant_id=$1", tenant).Scan(&endpoint, &enabled)
		if errors.Is(err, pgx.ErrNoRows) {
			respond(w, 200, map[string]any{"configured": false, "enabled": false})
			return
		}
		if err != nil {
			fail(w, err)
			return
		}
		respond(w, 200, map[string]any{"configured": true, "url": endpoint, "enabled": enabled})
		return
	}
	if (len(parts) == 1 && parts[0] == "config" || len(parts) == 2 && parts[0] == "config" && parts[1] == "reset-secret") && r.Method == http.MethodPost {
		tx, err := a.Store.DB.Begin(ctx)
		if err != nil {
			fail(w, err)
			return
		}
		defer tx.Rollback(ctx)
		// Serialize initial configuration and rotations without an unprotected first-row race.
		if _, err = tx.Exec(ctx, "SELECT pg_advisory_xact_lock(hashtextextended($1,7))", tenant); err != nil {
			fail(w, err)
			return
		}
		var endpoint string
		var enabled bool
		var encrypted []byte
		err = tx.QueryRow(ctx, "SELECT url,enabled,encrypted_secret FROM notification_configs WHERE tenant_id=$1 FOR UPDATE", tenant).Scan(&endpoint, &enabled, &encrypted)
		fresh := errors.Is(err, pgx.ErrNoRows)
		if err != nil && !fresh {
			fail(w, err)
			return
		}
		reset := len(parts) == 2
		if reset && fresh {
			fail(w, errNotFound)
			return
		}
		if !reset {
			var in struct {
				URL     string `json:"url"`
				Enabled bool   `json:"enabled"`
			}
			if decode(r, &in) != nil || notificationURL(in.URL) != nil {
				fail(w, errInvalid)
				return
			}
			endpoint, enabled = in.URL, in.Enabled
		}
		result := map[string]any{"configured": true, "url": endpoint, "enabled": enabled}
		if fresh || reset {
			secret := token()
			encrypted, err = a.Vault.seal(tenant+":outbound", []byte(secret))
			if err != nil {
				fail(w, err)
				return
			}
			result["signing_secret"] = secret
		}
		_, err = tx.Exec(ctx, `INSERT INTO notification_configs(tenant_id,url,enabled,encrypted_secret) VALUES($1,$2,$3,$4) ON CONFLICT(tenant_id) DO UPDATE SET url=excluded.url,enabled=excluded.enabled,encrypted_secret=excluded.encrypted_secret,updated_at=now()`, tenant, endpoint, enabled, encrypted)
		if err == nil {
			err = tx.Commit(ctx)
		}
		if err != nil {
			fail(w, err)
			return
		}
		respond(w, 200, result)
		return
	}
	if len(parts) == 1 && parts[0] == "deliveries" && r.Method == http.MethodGet {
		rows, err := a.Store.DB.Query(ctx, `SELECT event_id,status,attempts,status_code,last_error,next_at,created_at FROM notification_deliveries WHERE tenant_id=$1 ORDER BY event_id DESC LIMIT 100`, tenant)
		if err != nil {
			fail(w, err)
			return
		}
		defer rows.Close()
		items := []map[string]any{}
		for rows.Next() {
			var id int64
			var status, message string
			var attempts, code int
			var next, created time.Time
			if err = rows.Scan(&id, &status, &attempts, &code, &message, &next, &created); err != nil {
				fail(w, err)
				return
			}
			items = append(items, map[string]any{"event_id": id, "status": status, "attempts": attempts, "status_code": code, "last_error": message, "next_at": next, "created_at": created})
		}
		if err = rows.Err(); err != nil {
			fail(w, err)
			return
		}
		respond(w, 200, map[string]any{"data": items})
		return
	}
	if len(parts) == 3 && parts[0] == "deliveries" && parts[2] == "retry" && r.Method == http.MethodPost {
		id, err := strconv.ParseInt(parts[1], 10, 64)
		if err != nil || id < 1 {
			fail(w, errInvalid)
			return
		}
		tag, err := a.Store.DB.Exec(ctx, `UPDATE notification_deliveries SET status='pending',attempts=0,next_at=now(),last_error='',lease_until=NULL,lease_token=NULL,updated_at=now() WHERE tenant_id=$1 AND event_id=$2 AND status IN ('failed','dead') AND (lease_until IS NULL OR lease_until<now())`, tenant, id)
		if err != nil {
			fail(w, err)
			return
		}
		if tag.RowsAffected() == 0 {
			fail(w, errNotFound)
			return
		}
		respond(w, 200, map[string]bool{"queued": true})
		return
	}
	fail(w, errNotFound)
}
