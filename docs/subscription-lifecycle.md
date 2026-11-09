# Pausa, retomada e troca de plano

As rotas exigem a chave do projeto (`Authorization: Bearer ...`), JSON e `Idempotency-Key` em cada POST. Reenvie a mesma chave e o mesmo corpo quando a resposta se perder. Valores financeiros são centavos; créditos de uso são inteiros.

## Pausa e retomada

- `POST /v1/subscriptions/{id}/pause` com `{}` congela o tempo restante do ciclo.
- `POST /v1/subscriptions/{id}/resume` com `{}` desloca a âncora e a próxima cobrança pelo tempo pausado.
- `paused_at` indica a pausa; `status` mantém a situação financeira (`active` ou `past_due`). Retomar não quita pendências nem concede créditos.
- Cobranças já emitidas continuam sendo reconciliadas e podem ser pagas. Cancelamento é definitivo para novas emissões; não há retomada de assinatura cancelada.
- Alterações de plano pendentes ou em revisão impedem outras mudanças até a liquidação ou revisão. Isso evita divergência entre a autorização e o plano cobrado.

## Prévia e confirmação

`POST /v1/subscriptions/{id}/change-plan/preview`

```json
{"plan_id":"ID_NOVO_PLANO"}
```

A resposta informa `old_plan_id`, `plan_id`, `currency`, `unused_credit`, `amount_due`, `billing_credit_after`, `credits_to_grant` e `new_period_end`. A prévia é informativa; o tempo continua correndo e deve ser revisada antes de confirmar.

`POST /v1/subscriptions/{id}/change-plan`

```json
{"plan_id":"ID_NOVO_PLANO","max_amount":15000}
```

`max_amount` é o teto autorizado em centavos; use o `amount_due` da prévia. O servidor rejeita com 409 se o valor ultrapassar o teto. Para obter outra prévia ou confirmar um novo corpo, utilize uma nova chave de idempotência.

A troca exige assinatura ativa, não pausada, período atual pago, sem cobrança de ciclo pendente e plano de mesma moeda. É possível mudar entre períodos em dias e meses. O novo período tem duração integral do novo plano; quando houver valor a cobrar, a mudança só é aplicada após confirmação do provedor. A resposta inclui `change_id`, `charge_id` e `status` (`pending` ou `applied`). Consulte `GET /v1/subscriptions/{id}` e a cobrança para acompanhar.

## Cálculo

O valor não utilizado é o preço do plano atual multiplicado pela fração de tempo restante do período, com arredondamento para baixo em centavos. O valor a cobrar é o preço integral do novo plano menos esse abatimento e o crédito financeiro existente. Se resultar negativo, não há cobrança; a diferença fica como crédito financeiro da assinatura, abatido nas próximas renovações. Não é saque ou estorno para o cartão.

Exemplo ilustrativo: plano de R$ 100 com metade do período restante, troca para R$ 80. O abatimento é R$ 50 e a nova cobrança é R$ 30. Uma troca para R$ 20 gera R$ 30 de crédito financeiro. O instante real pode alterar centavos.

O ajuste dos créditos de uso é separado: créditos integrais do novo plano menos créditos proporcionais ao período antigo não utilizado (arredondados para cima). O ajuste pode ser negativo, inclusive deixando a carteira negativa se o comprador já consumiu os créditos. Essa regra impede criar créditos gratuitos por trocas repetidas. Não há concessão duplicada na cobrança de ajuste.

## Auditoria e recuperação

`billing_credit` é o saldo monetário da assinatura, diferente do saldo da carteira. `pending_change` apresenta a alteração pendente ou em revisão. Os eventos `subscription.change_pending`, `subscription.change_applied` e `subscription.change_review` permitem notificar o SaaS. A pausa e retomada emitem `subscription.paused` e `subscription.resumed`; cancelamento mantém `subscription.canceled`. Consulte `GET /v1/subscriptions/{id}/changes` e `GET /v1/subscriptions/{id}/billing-ledger` para os últimos 100 registros de alterações e do extrato monetário imutável.

Renovações totalmente cobertas por crédito têm `amount: 0`, `kind: "credit_settlement"` e `billing_credit_used` com o abatimento. Não há chamada ao provedor e o evento é `charge.paid`. Cobranças de ajuste têm `kind: "plan_change"`; as demais mantêm `kind: "payment"`. Não some abatimentos ao dinheiro recebido do provedor.

Recusa definitiva mantém o plano anterior. Um pagamento confirmado tardiamente após recusa, ou um estorno da cobrança que financiou a troca, exige revisão: o pagamento permanece registrado e a assinatura deixa de emitir ciclos automaticamente. Não há estorno automático nem resolução financeira automática desses casos; o operador precisa reconciliar os valores com o provedor.

O painel apresenta a prévia e pede confirmação antes de trocar o plano. Os endpoints de gestão são privados nesta instalação; a configuração do domínio/proxy para acesso entre servidores continua necessária.
