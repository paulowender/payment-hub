#!/usr/bin/env python3
"""Check public routing on an ephemeral loopback listener without requiring a public certificate."""
from pathlib import Path
import json
import subprocess
import time
import urllib.request
import urllib.error
import uuid
import sys
tunnel="--tunnel" in sys.argv
container_port="8081" if tunnel else "80"
config_file="Caddyfile.tunnel" if tunnel else "Caddyfile"

root=Path(__file__).resolve().parent.parent
name='paymenthub-route-check-'+uuid.uuid4().hex[:10]
command=['docker','run','-d','--rm','--name',name,'--network','paymenthub-local_default','-p','127.0.0.1::'+container_port,'-e','PUBLIC_WEBHOOK_HOSTNAME=http://gateway.test','-v',str(root/'deploy'/config_file)+':/etc/caddy/Caddyfile:ro','caddy:2-alpine']
subprocess.run(command,check=True,capture_output=True)
try:
 info=json.loads(subprocess.check_output(['docker','inspect',name],text=True))[0]
 port=info['NetworkSettings']['Ports'][container_port+'/tcp'][0]['HostPort']
 def status(path,method='GET'):
  req=urllib.request.Request('http://127.0.0.1:'+port+path,method=method,headers={'Host':'gateway.test','Content-Type':'application/json'},data=b'{}' if method=='POST' else None)
  try:
   with urllib.request.urlopen(req,timeout=2) as res:return res.status
  except urllib.error.HTTPError as err:return err.code
 for _ in range(40):
  try:
   if status('/console')==404:break
  except OSError:time.sleep(.1)
 else:raise AssertionError('Proxy did not start')
 assert status('/console')==404
 assert status('/admin/tenants','POST')==404
 assert status('/v1/customers')==404
 assert status('/healthz')==404
 path='/webhooks/pagarme/'+'a'*64
 assert status(path)==404
 assert status(path,'POST')==401
 assert status(path+'/extra','POST')==404
 checkout='/checkout/'+'b'*64
 assert status(checkout)==200
 assert status(checkout+'/data')==404
 assert status(checkout+'/pay','POST')==400
 assert status('/checkout/assets/checkout.js')==200
 assert status('/checkout/assets/checkout.css')==200
 assert status('/checkout/assets/app.js')==404
 assert status('/checkout/'+('b'*63))==404
 assert status(checkout+'/extra')==404
 assert status(checkout,'POST')==404
 portal='/portal/'+'c'*64
 assert status(portal)==200
 assert status(portal+'/data')==410
 assert status(portal+'/cancel','POST')==410
 assert status(portal+'/card','POST')==410
 assert status('/portal/assets/portal.js')==200
 assert status('/portal/assets/app.js')==404
 assert status(portal+'/extra')==404
 assert status('/v1/checkout-settings')==404
 print('PASS: exact webhook, checkout and portal routes; admin/console/API remain private')
finally:
 subprocess.run(['docker','rm','-f',name],check=True,capture_output=True)
