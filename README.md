# Payment Hub

Backend de orquestração de cobranças para conectar vários SaaS a Stripe e Pagar.me. Go + PostgreSQL, API JSON e worker persistente. Uma instalação atende vários projetos com chaves e dados separados.

**Entrega: aplicação local com backend e painel administrativo.** Inclui cobranças de cartão salvo, recorrência própria, planos imutáveis, carteira de créditos, extrato, eventos por cursor, idempotência, reconciliação e isolamento. Pagar.me foi verificado também com a conta sandbox fornecida. Stripe continua validado por contratos HTTP locais. Homologar produção separadamente.

## Painel local

Esta instalação está em `/home/wender/Projetos/WENDERTECH/payment-hub` e roda em **http://localhost:18080/console**. O painel usa Bootstrap 5.3.8 servido localmente, sem dependência de CDN no navegador.

- **Administrador:** entrar com ADMIN_SECRET da instalação; pode criar e selecionar projetos.
- **Projeto:** entrar com a chave de API daquele projeto; acesso apenas aos próprios dados e provedores.
- Acesso inicial da instalação configurada: arquivo privado `.local/access.json`, campo `project_api_key`. A chave administrativa está em `.env`. Não publique esses arquivos.
- Na primeira instalação, o administrador cria um projeto e guarda a chave exibida uma única vez.
- **Conexões:** selecionar Pagar.me ou Stripe, informar ambiente e chave, salvar e testar. Não é necessário editar env ou reiniciar para cadastrar credenciais de provedor.
- **Eventos recebidos:** feed das notificações autenticadas e dos eventos internos, atualizado a cada 5 segundos.
- **Cobranças/assinaturas/clientes/planos:** consultar e criar registros. Na carteira do cliente, consultar extrato, conceder e consumir créditos.

Credenciais de provedores são criptografadas com AES-256-GCM no PostgreSQL e nunca retornam para o navegador depois de salvas. Apenas a **chave mestra da instalação**, ENCRYPTION_KEY, fica em `.env`, junto do segredo administrativo e da senha do banco. Faça backup dessa chave: sem ela não será possível decifrar conexões existentes.

## Instalar outra instância

Requisitos: Docker com Compose e Python 3 para gerar segredos. Para desenvolvimento fora de Docker: Go 1.26 e PostgreSQL 17.

```sh
python3 scripts/setup.py
docker compose -p paymenthub-local up --build -d
curl http://localhost:18080/healthz
```

O setup preserva `.env` existente. Se estiver atualizando a versão inicial, acrescente ENCRYPTION_KEY com 32 bytes aleatórios codificados em 64 caracteres hexadecimais; nunca substitua uma chave que já protege conexões salvas. A porta é configurável por PAYMENT_HUB_PORT (padrão 18080).

Banco e API reiniciam automaticamente pelo Docker. O banco fica em volume persistente e não publica porta. `docker compose -p paymenthub-local down` preserva os dados; **não use down -v** se quiser preservá-los.

## Pagar.me sandbox desta instalação

A conta fornecida foi cadastrada pela API autenticada do painel e validada diretamente com Pagar.me. Testes concluídos: cobrança avulsa, replay idempotente, créditos e consumo, primeiro ciclo, renovação com origem, cancelamento da assinatura e estorno com reversão de créditos. A assinatura de teste foi cancelada para impedir novas cobranças.

Relatório sanitizado: [docs/sandbox-validation.json](docs/sandbox-validation.json).

Webhook receptor: `POST /webhooks/pagarme/{tenant_id}`, Basic Auth com o usuário/senha cadastrados na conexão. A tela Conexões apresenta a URL completa para copiar. Foram validados recebimento local e replay; não há uma URL pública configurada e, portanto, a entrega iniciada pelo próprio Pagar.me não foi homologada. Para isso, use HTTPS público encaminhando essa rota ao serviço local.

O payload apenas agenda reconciliação com o provedor; não altera saldo nem confirma pagamento por conta própria. A reconciliação periódica continua funcionando se não chegar webhook.

## Fluxo de integração

1. O seu backend cria um cliente local com `external_id` estável.
2. O frontend do SaaS usa o SDK/checkout do provedor para cadastrar e autorizar cartão; dados brutos de cartão nunca passam por esta API.
3. O backend envia `provider_customer` e `payment_method` (Stripe `pm_...`; Pagar.me `card_...`, ambos vinculados ao cliente no provedor).
4. Cria uma cobrança avulsa ou uma assinatura de plano. A API responde `queued`; isso ainda não significa pagamento.
5. Consulta a cobrança ou consome eventos até `paid`. Créditos comprados entram somente nessa confirmação.
6. O SaaS consome créditos pela API antes de liberar o uso do recurso. Se a resposta se perder, repete com a mesma chave de idempotência.

`API_KEY` deve ficar apenas no seu servidor. Ela permite conceder créditos promocionais, consumir saldo e criar cobranças dentro do projeto. Nunca exponha essa chave nem o segredo administrativo no navegador.

### Cliente e plano

```sh
export API_KEY='CHAVE_DO_PROJETO'
curl -X POST http://localhost:18080/v1/customers \
  -H "Authorization: Bearer $API_KEY" -H 'Idempotency-Key: customer-user-123' \
  -H 'Content-Type: application/json' -d '{"external_id":"user-123"}'

curl -X POST http://localhost:18080/v1/plans \
  -H "Authorization: Bearer $API_KEY" -H 'Idempotency-Key: plan-pro-v1' \
  -H 'Content-Type: application/json' \
  -d '{"name":"Pro mensal","amount":4990,"currency":"brl","credits":1000,"interval_unit":"month","interval_count":1}'
```

### Assinatura

```sh
curl -X POST http://localhost:18080/v1/subscriptions \
  -H "Authorization: Bearer $API_KEY" -H 'Idempotency-Key: subscription-user-123-v1' \
  -H 'Content-Type: application/json' \
  -d '{"customer_id":"ID_CLIENTE","plan_id":"ID_PLANO","provider":"stripe","provider_customer":"cus_...","payment_method":"pm_..."}'
```

Primeira cobrança no início; `start_at` opcional em ISO 8601 permite início futuro (trial). O cadastro aceita no máximo 10 anos à frente e até um minuto de tolerância no passado. Intervalos suportados: de 1 a 365 dias ou de 1 a 120 meses. Para cobrar anualmente, use 12 meses; quinzenalmente, 15 dias.

Âncoras são UTC: 31/jan → último dia de fevereiro → 31/mar. O ciclo seguinte espera o anterior ser pago. Após indisponibilidade, cobra somente um ciclo e pula os demais vencidos, sem acumular cobranças retroativas. Falha definitiva suspende novos ciclos (`past_due`). Cancelamento impede novas emissões, mas não desfaz cobranças já emitidas. Troca de plano com prorrateio e pausa/retomada estão disponíveis pela API e pelo painel. Consulte [gestão de assinaturas](docs/subscription-lifecycle.md) para as regras de crédito financeiro, ajuste dos créditos de uso e recuperação de casos em revisão.

### Compra avulsa de créditos

```sh
curl -X POST http://localhost:18080/v1/charges \
  -H "Authorization: Bearer $API_KEY" -H 'Idempotency-Key: checkout-order-123' \
  -H 'Content-Type: application/json' \
  -d '{"customer_id":"ID_CLIENTE","provider":"stripe","provider_customer":"cus_...","payment_method":"pm_...","amount":2000,"currency":"brl","credits":500}'
```

Para cobrança sem créditos, use `credits: 0`. BRL e USD no Stripe; BRL no Pagar.me. Valores em centavos, entre 1 e 99.999.999, sujeitos aos limites mínimos do provedor. Créditos inteiros de 0 a 1.000.000.000 por operação. O sistema não recebe números de cartões nem CVV.

### Carteira

```sh
curl http://localhost:18080/v1/wallets/ID_CLIENTE -H "Authorization: Bearer $API_KEY"
curl -X POST http://localhost:18080/v1/wallets/ID_CLIENTE/consume \
  -H "Authorization: Bearer $API_KEY" -H 'Idempotency-Key: usage-job-123' \
  -H 'Content-Type: application/json' -d '{"credits":10,"reference":"job-123"}'
```

`grant` tem o mesmo corpo e concede créditos promocionais sem pagamento. Não use `grant` para compra de créditos: informe os créditos na cobrança para serem liberados após pagamento. A referência é única por cliente e tipo de movimentação. Reutilizá-la com outra chave retorna conflito. Consumo concorrente não permite gastar acima do saldo. Um estorno parcial ou total/chargeback retira todos os créditos daquela compra; saldo pode ficar negativo se já consumidos. Essa política conservadora é deliberada; se o produto precisar de estorno proporcional, deverá adicionar uma política explícita antes de usá-la.

A carteira representa créditos de uso, sem saque, rendimento, transferência entre clientes ou resgate financeiro. Não é uma conta de dinheiro.

## API

Rotas de negócio exigem `Authorization: Bearer <api_key>`; todas as escritas também exigem `Idempotency-Key` de até 200 caracteres. Repetição com o mesmo caminho e corpo retorna a resposta persistida; mudar o corpo (inclusive sua representação JSON) com a mesma chave retorna 409. Não existe expiração automática das chaves locais.

| Método | Rota | Finalidade |
|---|---|---|
| GET | `/healthz` | Disponibilidade do banco |
| POST | `/admin/tenants` | Criar projeto e obter chave |
| POST | `/admin/tenants/{id}/rotate-key` | Revogar chave anterior e gerar nova; corpo `{}` |
| POST, GET | `/v1/customers` | Criar/listar clientes |
| POST, GET | `/v1/plans` | Criar/listar planos |
| POST, GET | `/v1/charges` | Criar/listar cobranças |
| POST, GET | `/v1/subscriptions` | Criar/listar assinaturas |
| GET | `/v1/{customers,plans,charges,subscriptions}/{id}` | Consultar recurso |
| POST | `/v1/subscriptions/{id}/cancel` | Cancelar recorrência; corpo `{}` |
| GET | `/v1/wallets/{customer_id}` | Saldo |
| POST | `/v1/wallets/{customer_id}/consume` | Consumir créditos |
| POST | `/v1/wallets/{customer_id}/grant` | Conceder créditos promocionais |
| GET | `/v1/ledger?customer_id=...&after=0` | Extrato paginado |
| GET | `/v1/events?after=0` | Eventos persistidos por cursor |

Listas retornam `{"data": [...]}`, até 100 itens. `limit` aceita 1–100. Nas listas de recursos, `after` é o último ID textual recebido, com ordenação por ID (não por data). Em eventos e extrato, `after` é o último ID numérico. Listas de recursos não são feeds de alterações; para acompanhar mudanças, use eventos.

Sucesso das escritas de negócio retorna 200, provisionamento retorna 201. Erros retornam `{"error":"codigo"}`: 400 entrada inválida/conta de provedor não configurada; 401 autenticação; 404 recurso inexistente ou de outro projeto; 409 idempotência, duplicidade ou saldo insuficiente; 500 falha interna. Limite do corpo: 1 MiB.

Contrato importável em ferramentas de API: [docs/openapi.json](docs/openapi.json).

### Metadata e integração com a Prontei

Clientes, planos, ofertas, sessões de checkout, assinaturas e cobranças aceitam `metadata`: um objeto JSON opcional de até 8192 bytes. O Hub guarda essa metadata de forma opaca, isolada por projeto, e a devolve nas criações, consultas, listagens e eventos aplicáveis. Não envie segredos, dados de cartão ou tokens. Metadata serve para correlação; nunca comprova pagamento nem autoriza acesso.

`POST /v1/offers` devolve `id` (oferta), `plan_id` (plano recorrente, vazio em compra avulsa) e `metadata`. Para migrações na renovação existem `POST /v1/subscriptions/{id}/change-plan/schedule` e `/schedule/cancel`; o plano atual permanece vigente até a cobrança da renovação no plano destino ser confirmada. Consulte [integração Prontei](docs/prontei-integration.md) para chaves idempotentes, payloads, eventos e reconciliação.

### Eventos, confirmação e autenticação adicional

Consuma `/v1/events?after=ULTIMO_ID`, processe cada evento, depois persista o último ID concluído. Os eventos são duráveis e a publicação dentro de cada projeto respeita a ordem de commit. Retente seus próprios efeitos com deduplicação pelo ID. Não avance o cursor antes de concluir o processamento.

Eventos de cobrança: `charge.queued`, `charge.pending`, `charge.requires_action`, `charge.paid`, `charge.failed`, `charge.reversed`, `charge.manual_review`. Existem também eventos de assinatura, inclusive criação, pausa, cancelamento, troca imediata e troca agendada. Eventos financeiros e de assinatura incluem identificadores correlacionáveis e metadata por recurso (`charge_metadata`, `customer_metadata`, `subscription_metadata`, `plan_metadata`, `checkout_session_metadata` e `offer_metadata`) quando o recurso é aplicável. Eles não incluem credenciais do provedor, meio de pagamento ou snapshot do comprador. Veja [notificações](docs/notifications.md).

`subscription.created` confirma somente a criação do contrato local; não comprova pagamento. Ative acesso apenas após `charge.paid` persistido e validado, ou após reconciliação autenticada que confirme o mesmo estado. Créditos no evento pertencem à cobrança, não representam o saldo atual; consulte a carteira. A ausência de um evento intermediário não significa erro: pagamento síncrono pode ir de `queued` diretamente para `paid`.

Quando Stripe exigir autenticação adicional, consulte a cobrança autenticada para obter `client_secret` e conclua o fluxo pelo SDK do Stripe no SaaS. Nunca exponha a chave da API do Hub para isso. A versão inicial exige esse fluxo no produto integrador e autorização prévia para pagamentos off-session.

A reconciliação consulta o provedor a cada 30s em pagamentos pendentes e a cada 6h nos pagos. Esta versão recebe webhooks autenticados do Pagar.me, entrega notificações HMAC configuradas por projeto e mantém o feed de eventos como fallback e fonte de reconciliação. Portanto, a atualização é eventual. O worker processa um lote limitado de dez operações por passagem; dimensione instâncias de acordo com a latência e o volume.

## Executar e testar sem Docker da aplicação

```sh
go mod download
export DATABASE_URL='postgres://usuario:senha@localhost:5432/paymenthub?sslmode=disable'
export ADMIN_SECRET='SEGREDO_ALEATORIO_COM_PELO_MENOS_32_CARACTERES'
export ENCRYPTION_KEY='64_CARACTERES_HEXADECIMAIS_DA_CHAVE_MESTRA'
export LISTEN_ADDR='127.0.0.1:18080'
go run ./cmd/server
```

`LISTEN_ADDR` opcional (padrão `:8080`). Migrações são aplicadas na inicialização sob lock; para aplicar e sair: `go run ./cmd/server migrate`. O schema está embutido no binário. Antes de futuras alterações, introduzir migrações numeradas a partir da versão 2; não alterar silenciosamente tabelas existentes em produção.

```sh
go test -race ./...
go vet ./...
go build ./cmd/server
# Banco descartável: os testes criam e removem schemas exclusivos.
TEST_DATABASE_URL='postgres://usuario:senha@localhost:5432/paymenthub_test?sslmode=disable' go test -race -count=1 -v ./...
```

Sem `TEST_DATABASE_URL`, os testes de integração são pulados; isso não prova a integração com o banco. A credencial de testes precisa de permissão para criar e remover schemas. Nunca apontar esse teste para produção.

## Operação e próximos passos

Consulte [arquitetura](docs/architecture.md), [plano](docs/plan.md), [operação](docs/operations.md) e [validação da entrega](docs/validation.md).

Próximas entregas: homologação real do Stripe e de produção, onboarding de cartões/checkout, Pix e boleto, notificações para os SaaS com assinaturas e retentativas, dunning e atualização de meios de pagamento. Depois, conforme demanda: vencimento de créditos, cobrança por consumo, cupons, impostos/notas e prorrateio. Esses recursos não estão implementados neste MVP.

## Entrada pública para webhook

Foi preparado gateway HTTPS limitado a notificações, com hostname provisório `paymenthub.200-152-10-86.sslip.io`. O painel segue local. A emissão do certificado ainda depende da liberação de rede: a certificadora recebeu timeout nas portas 80/443. Siga [a configuração pública e a pendência de firewall](docs/public-webhook.md). Não considerar o webhook externo homologado enquanto esse teste não passar.

### Túnel de desenvolvimento ativo

O webhook agora tem HTTPS público temporário via Cloudflare Quick Tunnel. A URL correta é apresentada em Conexões → Pagar.me → Editar conexão. Entrega externa autenticada e replay foram validados; o endpoint ainda precisa ser cadastrado no painel do Pagar.me. O painel administrativo permanece local. O endereço pode mudar se o túnel for recriado; detalhes e comandos em docs/public-webhook.md.

## Aparência
Tema escuro ativado por padrão, incluindo login e formulários. A lateral permite alternar para Tema claro; a escolha fica salva no navegador. Após atualizar uma instalação já aberta, recarregar a página para carregar os novos arquivos.

## Diagnóstico de requisições
Em Eventos recebidos, a captura temporária permite inspecionar cabeçalhos e o formato do payload sem expor segredos. O modo de sandbox foi aberto por 15 minutos e até 20 requisições; capturas sem autenticação não processam pagamentos. Veja docs/diagnostics.md.

## Checkout com a marca do projeto

Acesse **Checkout e marca** no painel para configurar a identidade, cadastrar ofertas e gerar links de compra. Cartão Pagar.me, cobranças avulsas, assinaturas e créditos; chave pública por projeto em **Conexões**. Consulte [integração e domínio próprio](docs/checkout.md).

## Pagamentos e operação

Checkout configurável com cartão parcelado, Pix e boleto Pagar.me; recorrência flexível por dias ou meses; histórico detalhado nas cobranças; portal do comprador em Assinaturas; notificações HMAC em Notificações aos sistemas. [Guia desta expansão](docs/payment-expansion.md). O boleto está implementado, mas depende de habilitação no provedor; está desativado nesta conta sandbox após HTTP 412.

## Simulação de recusas e conciliação

[Guia dos simuladores](docs/sandbox-simulators.md): Pix e cartão recusados, boleto a menor/a maior/sem conciliação, processamento, estorno e chargeback. Motivos e códigos sanitizados aparecem no histórico; pagamento parcial não libera créditos e continua sendo verificado.

## Gestão por API

[Provedores e notificações](docs/management-api.md) podem ser configurados pela chave do projeto, com idempotência e segredos criptografados. [Pausa, retomada e troca de plano](docs/subscription-lifecycle.md) têm prévia financeira e confirmação. As rotas estão descritas em [OpenAPI](docs/openapi.json).
