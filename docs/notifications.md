# Notificações de saída

Cada projeto pode cadastrar um destino HTTPS no console. Apenas eventos criados enquanto a configuração estiver ativa entram na fila. Desativar ou editar o destino afeta novos eventos; entregas existentes mantêm destino, conteúdo e chave originais. A chave fica criptografada e aparece somente na criação ou redefinição. Guarde a chave anterior até esvaziar a fila anterior à troca.

Rotas autenticadas do console, prefixadas por `/console/api/projects/{tenant}/notifications`:

- `GET config`: configuração sem a chave.
- `POST config`: JSON `{"url":"https://seu-dominio.com/webhook","enabled":true}`. A primeira resposta inclui `signing_secret`.
- `POST config/reset-secret`: gera e retorna uma nova `signing_secret`.
- `GET deliveries`: últimas 100 entregas com estado, tentativas, código HTTP e erro resumido.
- `POST deliveries/{event_id}/retry`: reenvia entregas com estado `failed` ou `dead`, exclusivo do administrador.

O POST de entrega contém `id`, `tenant_id`, `type`, `created_at`, `version: 1` e `data`. Identifique duplicatas pelo `id`, que permanece igual nos reenvios. A entrega tem garantia de pelo menos uma tentativa; interrupções após o receptor aceitar podem causar duplicatas. Implemente processamento idempotente no receptor.

## Dados para correlação

Eventos de cobrança incluem `charge_id`, `customer_id`, `status`, `credits`, `charge_metadata` e `customer_metadata`. Quando relacionados a uma assinatura, incluem também `subscription_id`, `plan_id`, `subscription_metadata` e `plan_metadata`. Quando originados por checkout, podem incluir `session_id`, `offer_id`, `reference`, `checkout_session_metadata` e `offer_metadata`.

Eventos de assinatura incluem `subscription_id`, `customer_id`, `plan_id`, `status` e as metadata da assinatura, cliente e plano. Checkout/oferta aparecem somente quando realmente relacionados. Campos específicos, como `charge_id`, `change_id`, `reason` e `billing_credit_used`, aparecem apenas no evento aplicável. Metadata ausente é `{}` no recurso existente; recursos não relacionados são omitidos. O payload usa uma lista explícita e nunca inclui `provider_customer`, `payment_method`, IDs internos do provedor, `client_secret`, fingerprint, dados do comprador ou snapshot de checkout.

Exemplo reduzido de confirmação:

```json
{
  "id": 1842,
  "tenant_id": "prontei",
  "type": "charge.paid",
  "created_at": "2026-11-10T12:00:00Z",
  "version": 1,
  "data": {
    "charge_id": "ch_...",
    "customer_id": "cus_...",
    "subscription_id": "sub_...",
    "plan_id": "plan_...",
    "status": "paid",
    "charge_metadata": {"source":"prontei"},
    "subscription_metadata": {"establishment_id":"42"},
    "plan_metadata": {"plan_version_id":"7"}
  }
}
```

Eventos de troca agendada acrescentam `source_plan_id`, `target_plan_id`, `effective_at`, `scheduled_change_status`, `scheduled_change_metadata`, cobrança/estado quando vinculados e `payment_review_at` quando existir. `subscription.created` não prova pagamento: o integrador deve ativar acesso apenas após persistir `charge.paid` válido ou confirmar o mesmo estado por reconciliação.

Para autenticar a entrega, use o corpo HTTP original sem recodificar JSON. Calcule HMAC-SHA256 com a chave e a sequência `X-PaymentHub-Timestamp + "." + corpo`. Compare em tempo constante com o hexadecimal após `sha256=` em `X-PaymentHub-Signature`. Valide também uma janela curta de tempo (por exemplo, cinco minutos). `X-PaymentHub-Event-ID` repete o identificador do corpo.

Uma resposta 2xx conclui a entrega. Falhas, incluindo redirecionamentos, são repetidas até oito tentativas, com intervalos de 30, 60, 120, 240, 480, 960 e 1920 segundos. Depois, a entrega fica `dead` para análise e reenvio manual. Respostas do receptor não são armazenadas. Apenas HTTPS na porta padrão é aceito; endereços internos, locais e reservados são bloqueados também na resolução DNS da conexão. O receptor deve apresentar um certificado TLS válido e responder em até cinco segundos.
