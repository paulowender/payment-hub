# Integração Prontei

Este é o contrato do backend Prontei com o Payment Hub. A Prontei mantém planos comerciais, direitos e projeção local de acesso; o Hub mantém preço/ciclo contratado, checkout, cobranças, renovações e estado financeiro.

## Autenticação e idempotência

- Use `Authorization: Bearer <chave exclusiva do projeto>` somente no servidor.
- Todo POST de negócio exige JSON e `Idempotency-Key` (1–200 caracteres).
- Reenvie a mesma chave e o mesmo corpo quando houver timeout. O Hub devolve a resposta persistida sem duplicar recursos; mesma chave com corpo/caminho diferente retorna 409.
- Não registre chave, URL temporária de checkout/portal, dados de cartão ou credenciais Pagar.me.

Chaves sugeridas:

| Operação | Chave |
|---|---|
| plano | `prontei-plan-<versao_id>-v<versao>` |
| oferta | `prontei-offer-<versao_id>-v<versao>` |
| customer | `prontei-establishment-<id>` |
| checkout | `prontei-checkout-<assinatura_local_id>` |
| migração | `prontei-migration-<item_id>` |
| cancelamento da migração | `prontei-migration-cancel-<item_id>` |

## Metadata

`customers`, `plans`, `offers`, `checkout sessions`, `subscriptions` e `charges` aceitam `metadata`. O campo é opcional; omitido retorna `{}`. Deve ser objeto JSON (não array/null/escalar) e ter no máximo 8192 bytes serializados. O Hub preserva metadata por recurso e não a herda implicitamente. Sessões guardam seu próprio snapshot. PostgreSQL JSONB pode normalizar ordem/formatação das chaves.

Use apenas IDs locais e contexto não secreto:

```json
{"source":"prontei","entity":"plan_version","local_id":"42","version":3}
```

Metadata é opaca: nunca decide preço, tenant, autorização ou estado financeiro. Não envie segredo, token, cartão, CVV ou documento desnecessário.

Os seis recursos devolvem `metadata` na criação, consulta e listagem autenticadas. `POST /v1/offers` devolve também `id` e `plan_id`; ao criar oferta recorrente inline, `plan_id` é o plano gerado. Compra avulsa usa string vazia.

## Publicar uma versão de plano

1. Persista a versão local como pendente e gere seu ID estável.
2. `POST /v1/plans` com preço em centavos, `currency: "brl"`, créditos, período e metadata da versão. Use `prontei-plan-<id>-v<versao>` e salve o `id` retornado.
3. `POST /v1/offers` referenciando esse `plan_id`, com nome, descrição e metadata própria. Use a chave distinta `prontei-offer-<id>-v<versao>` e salve o `id` da oferta.
4. Publique somente após salvar `payment_hub_plan_id` e `payment_hub_offer_id`. Se uma resposta se perder, repita exatamente a mesma chamada com a chave daquela etapa.

As duas chamadas são intencionais: metadata pertence ao recurso e não é herdada. Criar plano inline em `POST /v1/offers` é suportado, mas deixa a metadata do plano gerado como `{}`; para a Prontei, prefira criar plano e oferta separadamente.

Exemplo:

```json
{
  "name":"Profissional mensal",
  "amount":19700,
  "currency":"brl",
  "credits":0,
  "interval_unit":"month",
  "interval_count":1,
  "metadata":{"source":"prontei","entity":"plan_version","local_id":"42","version":3}
}
```

Depois, crie a oferta vinculada:

```json
{
  "name":"Profissional mensal",
  "description":"Agenda e automações",
  "plan_id":"plan_retornado_acima",
  "metadata":{"source":"prontei","entity":"plan_offer","local_id":"42","version":3}
}
```

Arquivar a oferta impede novos checkouts e não altera contratos existentes.

## Customer e checkout

1. Crie/reutilize customer com `external_id: "prontei:estabelecimento:<id>"` e metadata local.
2. Crie `POST /v1/checkout-sessions` com `offer_id`, `customer_id`, referência estável e metadata própria.
3. Entregue ao navegador somente a `url` retornada.
4. O retorno do navegador exibe “processando”; não ativa a assinatura.
5. `subscription.created` indica que o contrato foi criado, não que a cobrança foi paga.
6. Ative a projeção local somente após `charge.paid` autenticado e persistido, ou reconciliação autenticada que confirme a cobrança.

As respostas de criação são:

- customer: `id`, `external_id`, `metadata`;
- plano: `id`, `metadata`;
- oferta: `id`, `plan_id`, `metadata`;
- checkout: `id`, `path`, `url`, `expires_at`, `metadata`;
- assinatura: `id`, `status`, `start_at`, `metadata`;
- cobrança: `id`, `status`, `metadata`.

## Webhook e feed de eventos

Configure o destino HTTPS do projeto no Hub. Verifique o corpo original com HMAC-SHA256 sobre `timestamp + "." + body`, compare em tempo constante, valide janela de cinco minutos e deduplique por `id`/`X-PaymentHub-Event-ID`. Grave inbox e efeito local na mesma transação; responda 2xx somente após commit.

As entregas são pelo menos uma vez e preservam ID/corpo nas retentativas. Em paralelo, `/v1/events?after=<cursor>` permite recuperação. Persista o cursor só depois do efeito local. Consulte periodicamente assinaturas/cobranças não terminais para recuperar evento perdido; um estado local mais novo não deve ser rebaixado por resposta antiga.

Campos de correlação e exclusões de segredo estão em [notificações](notifications.md). Eventos desconhecidos devem ser guardados como ignorados para compatibilidade futura, sem loop infinito de retentativa.

## Migração na renovação

Agende uma assinatura por item:

```http
POST /v1/subscriptions/{id}/change-plan/schedule
Idempotency-Key: prontei-migration-91
```

```json
{"plan_id":"plan_destino","metadata":{"source":"prontei","migration_item_id":"91"}}
```

O Hub responde `change_id`, `status: "scheduled"` e `effective_at`. `GET /v1/subscriptions/{id}` expõe `pending_scheduled_change`. Até `charge.paid`, a assinatura continua no plano origem. Na renovação o Hub cobra preço/moeda/créditos do destino, mas só aplica o plano após confirmação; falha ou revisão mantém o plano antigo.

Antes do vínculo da cobrança, cancele com `POST /v1/subscriptions/{id}/change-plan/schedule/cancel`, corpo `{}` e chave própria. Depois do vínculo, acompanhe o estado financeiro. Cancelar a assinatura cancela também o agendamento. Pagamento tardio após pausa/cancelamento exige revisão manual e não concede direitos automaticamente.

Consuma `subscription.change_scheduled`, `subscription.change_schedule_canceled`, `subscription.change_schedule_applied` e `subscription.change_schedule_failed`. Use `change_id` e `scheduled_change_metadata` para atualizar o item local de migração; não conclua pela volta do navegador nem apenas por data.

## Limites de confiança

- Recarregue plano/preço publicado no servidor; não confie em valor ou IDs enviados pelo navegador.
- O Hub é fonte financeira; a Prontei autoriza requisições normais pela projeção local, sem chamada síncrona por acesso.
- Metadata correlaciona, mas nunca substitui tabelas locais ou o tipo/estado do evento.
- Não libere IA, WhatsApp ou automações antes de pagamento confirmado.
- Em indisponibilidade do Hub, mantenha a operação existente conforme o estado local e reprocese criações com a mesma chave idempotente.
