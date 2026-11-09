# Webhook público — ambiente de desenvolvimento

## Modo ativo: túnel HTTPS temporário

O acesso externo foi validado por túnel Cloudflare Quick Tunnel, que aponta para um proxy interno com allowlist de rota. A entrada direta por IP ainda não foi validada: a consulta via 200.152.10.86 apresentou certificado autoassinado do modem ZTE e as validações ACME externas expiraram por timeout. O usuário confirmou ter executado o script UFW e que a DMZ aponta para 192.168.1.200 (cabo).

URL ativa:

https://regulatory-members-tomatoes-focused.trycloudflare.com/webhooks/pagarme/19830b3d1e7f498bd28a761916bfab7f30ce2edeb66d13d7c7f0a152733e0489

A URL também é apresentada no painel local, em Conexões → Pagar.me → Editar conexão. Cadastre-a no painel Pagar.me com os mesmos usuário e senha Basic Auth da conexão. Esse cadastro no Pagar.me ainda não foi realizado, pois não há sessão administrativa dessa conta disponível.

Testado via HTTPS público com validação normal do certificado: entrega autenticada, repetição deduplicada e recebimento visível no banco/painel. Sem autenticação retorna 401. Painel, admin, API e healthz retornam 404 publicamente. Isso não equivale a confirmar entrega originada pelo próprio Pagar.me.

## Serviços

```sh
docker compose -p paymenthub-local -f compose.yaml -f compose.public.yaml -f compose.tunnel.yaml up -d --build
```

No modo túnel, compose.tunnel.yaml remove as publicações 80/443 do gateway e usa um listener interno na porta 8081. A API continua publicada somente em 127.0.0.1:18080. A conexão do cloudflared é de saída, sem depender de encaminhamento de portas de entrada.

PUBLIC_WEBHOOK_BASE_URL em .env contém apenas o endereço público temporário. Credenciais de provedores continuam cifradas no banco, sem transferência para arquivos de configuração do túnel. O tráfego HTTPS termina na Cloudflare e segue pelo túnel até o receptor.

## Limitação importante

Quick Tunnel é para desenvolvimento: o hostname muda quando o processo é recriado e não há garantia de disponibilidade. Se isso acontecer, consulte a nova URL nos logs, atualize PUBLIC_WEBHOOK_BASE_URL em .env, recrie somente a API e atualize o cadastro no provedor. Para endereço permanente, usar domínio próprio/túnel nomeado ou corrigir o encaminhamento de entrada no modem/operadora.

```sh
docker compose -p paymenthub-local -f compose.yaml -f compose.public.yaml -f compose.tunnel.yaml logs webhook-tunnel
docker compose -p paymenthub-local -f compose.yaml -f compose.public.yaml -f compose.tunnel.yaml up -d api
python3 scripts/check-gateway.py --tunnel
```

Referência: https://developers.cloudflare.com/tunnel/get-started/quick-tunnels/

---

## Histórico da tentativa por IP direto

# Webhook público — configuração local

IP de saída confirmado por dois serviços: 200.152.10.86.
Hostname provisório: paymenthub.200-152-10-86.sslip.io, que resolve para esse IP.

A entrada HTTPS usa Caddy com certificado automático e renovação persistida. Apenas POST /webhooks/pagarme/{tenant_id}, com ID hexadecimal de 64 caracteres, é encaminhado à API. Painel, /admin, /v1, /healthz e outros caminhos retornam 404 na entrada pública. A autenticação Basic Auth do projeto continua obrigatória.

## Subir ou atualizar

```sh
docker compose -p paymenthub-local -f compose.yaml -f compose.public.yaml up -d --build
```

A API administrativa permanece em 127.0.0.1:18080. As portas TCP 80 (validação ACME/redirect) e 443 (webhook HTTPS) são publicadas pelo gateway. Nenhuma regra existente do firewall foi apagada ou desabilitada. PUBLIC_WEBHOOK_HOSTNAME em .env é um nome público, não uma credencial de provedor.

## Liberação pendente

O UFW está habilitado; consultar/alterar regras exige senha sudo. O agente não recebeu essa senha e não alterou o UFW. Script preparado:

```sh
sudo sh /home/wender/Projetos/WENDERTECH/payment-hub/scripts/open-webhook-firewall.sh
```

A certificadora tentou validar HTTP-01 e TLS-ALPN-01 externamente e recebeu timeout nas portas 80 e 443. Portanto, o certificado público e o recebimento externo ainda não foram confirmados. Conectar ao próprio IP de dentro da rede não substitui esse teste externo.

Confirmar também a DMZ/encaminhamento do roteador. A máquina tem 192.168.1.200 no cabo e 192.168.1.25 no Wi-Fi; o destino da DMZ precisa corresponder ao endereço correto. Se a operadora bloquear 80/443 ou houver CGNAT, liberar UFW não bastará.

## Verificação

```sh
python3 scripts/check-gateway.py
docker compose -p paymenthub-local -f compose.yaml -f compose.public.yaml logs --tail=40 gateway
```

O teste de roteamento usa HTTP apenas num listener descartável de loopback, sem reduzir o TLS da entrada pública. Confirma bloqueio do painel/API e exigência de autenticação no webhook. A configuração Caddy passou em caddy validate e os 25 testes Go passaram com PostgreSQL real e detector de concorrência.

Depois de obter certificado válido, copiar a URL completa apresentada em Conexões no painel do Hub para o painel Pagar.me, habilitando o mesmo usuário/senha Basic Auth. O cadastro do endpoint no painel Pagar.me ainda não foi realizado: não há sessão administrativa dessa conta disponível.

Se o IP público mudar, atualizar PUBLIC_WEBHOOK_HOSTNAME, recriar gateway/API com o comando acima e atualizar a URL no provedor. O hostname sslip.io é provisório para desenvolvimento; para uma operação permanente, substituir por domínio próprio.

## Mudança do túnel em 28/10/2026

O túnel reiniciou e o endereço anterior deixou de resolver. Use sempre a URL atual exibida em Conexões → Pagar.me; atualize também o cadastro no painel do provedor. O túnel atual expõe as rotas públicas do checkout além do webhook, mantendo console/admin/API privados. Domínio definitivo elimina essa troca de endereço a cada reinício do túnel temporário.
