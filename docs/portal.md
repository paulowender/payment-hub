# Portal do comprador

A API autenticada `POST /v1/subscriptions/{id}/portal` cria um link pessoal para uma única assinatura Pagar.me do projeto autenticado. A resposta contém `id`, `path`, `url` e `expires_at`. Cada link tem 256 bits de entropia e dura 24 horas. Emitir outro link revoga os anteriores da mesma assinatura. O projeto deve entregar o link ao comprador por seu próprio canal autenticado; o link é uma credencial e não deve aparecer em logs, ferramentas de análise ou páginas públicas.

`GET /portal/{token}` abre o portal. `GET /portal/{token}/data` retorna marca, dados mínimos da assinatura, permissões e no máximo dez cobranças, sem identidade do comprador, identificadores do provedor ou segredos. Os únicos arquivos públicos são `/portal/assets/portal.js` e `/portal/assets/portal.css`.

`POST /portal/{token}/cancel` recebe `{}` e cancela futuros ciclos sem alterar cobranças existentes ou créditos. `POST /portal/{token}/card` recebe `card_token`, `buyer` (mesmos campos do checkout) e `recurring_consent: true`. O navegador envia número, validade e CVV diretamente à Pagar.me; o servidor aceita apenas token e endereço validado. O consentimento fica em `portal_sessions.consent_at`. A atualização muda somente o cartão da assinatura para cobranças futuras. Não altera cobranças em andamento, não gera cobranças, não liquida pendências e não reativa `past_due`. Assinaturas canceladas não permitem atualização de cartão.

As permissões `BuyerCancel` e `BuyerUpdateCard` são consultadas novamente em cada ação. Os POSTs exigem JSON estrito, rejeitam campos desconhecidos e origens diferentes. Sem Origin, a fronteira JSON e a ausência de cookies/CORS impedem formulários cross-site. O proxy deve sobrescrever `X-Forwarded-Proto`. Cada sessão aceita no máximo dez tentativas de ação, contadas de forma durável mesmo quando o provedor falha. Sessão expirada, revogada ou esgotada exige emissão de novo link.

## Uso no painel e integração

Em **Assinaturas → Portal do comprador**, gere, abra ou copie o link. A API recebe `{}` com `Authorization: Bearer CHAVE_DO_PROJETO` e `Idempotency-Key`; o SaaS entrega o link ao seu usuário autenticado. Inclua `/portal/*` no proxy de seu domínio próprio junto com `/checkout/*`.

Configure `buyer_cancel` e `buyer_update_card` em `/v1/checkout-settings` ou no painel. A troca do cartão reinicia a origem de recorrência para um novo cartão quando necessário, sem usar a origem de outro cartão. Cobranças já emitidas continuam com o cartão e a origem que tinham ao serem criadas.

Sandbox: emissão do link, atualização tokenizada de cartão, consulta de histórico e cancelamento pelo navegador foram validados. A renovação foi antecipada apenas para a assinatura descartável de teste, e os dois ciclos foram estornados.
