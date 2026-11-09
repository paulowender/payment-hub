# Validação da aplicação local — 27/10/2026

## Backend e painel

25 testes Go aprovados com PostgreSQL real e detector de concorrência, sem casos pulados. Inclui calendário ancorado, idempotência concorrente, consumo de saldo, isolamento, lease, assinatura/cancelamento, períodos perdidos, troca de credencial, sessões, revogação, CSRF, criptografia com binding por tenant e detecção de adulteração, persistência das conexões e webhook autenticado/deduplicado.

A aplicação e o banco foram iniciados em Docker Compose. O painel foi verificado no navegador, com Bootstrap local, login, conexão configurada, teste de conexão, formulários, tabelas e feed de eventos. Teste de regressão da troca de projeto reproduziu uma gravação de credencial no destino errado com formulário antigo e requisições de leitura interrompidas; a correção vincula formulário ao projeto de origem, limpa a tela na troca e bloqueia o envio obsoleto.

## Sandbox real Pagar.me

A conta sandbox fornecida pelo usuário foi usada exclusivamente no ambiente test. Cartão oficial do simulador enviado diretamente ao provedor; o Hub recebeu somente IDs de cliente/cartão.

Confirmados:
- Autenticação e teste de conexão pelo painel.
- Cobrança avulsa de R$ 10,00, com confirmação real do pedido.
- Repetição da criação com a mesma chave devolvendo a mesma cobrança.
- Liberação de 100 créditos e consumo de 25 créditos.
- Primeiro ciclo de assinatura de R$ 10,00, liberando 50 créditos.
- Renovação de outro ciclo de R$ 10,00, com referência à cobrança inicial, liberando 50 créditos.
- Cancelamento da assinatura para interromper novos ciclos.
- Estorno real da cobrança avulsa, com retirada dos 100 créditos e saldo final de 75 créditos.
- Entrega local autenticada de webhook, repetição e exibição no feed. Payload não altera saldo sem reconciliação.

Para exercitar a renovação sem aguardar um dia, apenas o next_at da assinatura descartável de sandbox foi antecipado no banco local. O worker realizou a emissão e a cobrança pelo fluxo normal. A assinatura foi cancelada ao final.

IDs operacionais sanitizados em sandbox-validation.json. Nenhuma chave do provedor, senha ou dado bruto de cartão está incluído nesse relatório.

## Limites

Não houve cobrança de produção. Stripe continua com testes de contrato HTTP locais; nenhuma chave Stripe foi fornecida. Entrega externa de webhook iniciada pelo Pagar.me não foi validada porque não há URL HTTPS pública configurada. Não foram realizados teste de carga, auditoria regulatória ou certificação de produção.

Serviços locais permanecem rodando em http://localhost:18080/console. Credenciais de provedor ficam cifradas no PostgreSQL; chave mestra e segredo administrativo são segredos da instalação, mantidos fora do código.

Verificação visual final: viewport móvel de 390px com largura do documento de 390px, tabelas com rolagem própria; desktop de 1440px. Console do navegador sem erros após navegação normal. Salvamento de conexão no formulário preservou segredos e novo teste real de conexão foi aprovado.

## Preparação de entrada pública
IP 200.152.10.86 confirmado. Caddy configurado, validado e iniciado com allowlist de rota; teste HTTP descartável comprovou que painel/admin/API não são encaminhados e que o webhook exige autenticação. Certificação externa não concluída: validações ACME HTTP-01/TLS-ALPN-01 retornaram timeout de conexão. UFW habilitado exige senha sudo; script específico foi preparado e informado ao usuário. Teste Go atualizado para URL pública do painel aprovado com a suíte de 25 casos.

## Túnel de desenvolvimento
Depois da confirmação do script de firewall, a consulta ao IP ainda apresentou certificado do modem ZTE. Foi iniciado túnel Cloudflare restrito ao receptor. O teste HTTPS público validou certificado, entrega autenticada 200, repetição com apenas um recibo persistido, e 404 para painel/admin/API/healthz. URL exibida no painel local, sem retornar credenciais. Entrega iniciada pelo Pagar.me segue pendente de cadastro no dashboard; endereço temporário pode mudar ao recriar o túnel.

## Teste após configuração no Pagar.me
Nova cobrança de sandbox paga e estornada (pedido or_xVq4XjlSMnSyD1zQ). Nenhum recibo nativo correlacionado chegou durante 60s; a consulta inicial filtrada não retornou registros, mas a consulta autenticada sem filtro confirmou seis entregas para a URL correta com HTTP 401. HTTPS externo do receptor continua respondendo 401 sem credenciais, conforme esperado. O envio ocorre na conta correta e alcança o endpoint; a autenticação Basic Auth no cadastro do Pagar.me precisa corresponder às credenciais salvas na conexão do Hub. O teste não foi marcado como aprovado. Reconciliação do estorno antecipada após o teste, sem confiar em payload sintético.

## Tema escuro e novo teste de autenticação
Tema escuro verificado em sessão nova (login), sessão autenticada, campos e diálogos. Alternância claro/escuro persiste após recarregar. Layout móvel sem overflow e botão de tema alcançável em janela de 493px de altura. Reenvio solicitado pela API do provedor e novo pagamento de sandbox também geraram 401; diagnóstico local mostrou authorization_present=false e basic_auth_parsed=false, com configuração carregada corretamente. Novo pagamento de sandbox estornado. A autenticação no envio do Pagar.me ainda precisa ser habilitada/corrigida; não foi removida a proteção do receptor.

## Diagnóstico temporário
Captura real de webhook sem autenticação, redigida e isolada do processamento financeiro, visualizada no painel. Testes cobrem sanitização do JSON/cabeçalhos, isolamento por projeto, ausência de alteração de saldo/recibos, expiração, limite de 20 e proibição em live. O JSON nativo está no formato esperado; Authorization não foi recebido. Modo de captura temporário não equivale a autenticação homologada.

## Checkout personalizável — 28/10/2026

34 testes passaram em PostgreSQL com race detector, nenhum ignorado; go vet e verificações de sintaxe JavaScript passaram. Marca e oferta de demonstração cadastradas pelo painel. Fluxos avulso e recorrente no navegador usam provedor simulado; cartão bruto não chegou ao Hub, consentimento recorrente obrigatório e telas pendente/paga verificadas. Celular 390px sem rolagem horizontal. Gateway direto e túnel permitem apenas checkout/webhook; console/admin/API públicos retornam 404. Ver docs/checkout-validation.json. Teste real com chave pública Pagar.me permanece pendente.

## Checkout Pagar.me real e autenticação confirmados — 28/10/2026

Com a chave pública corrigida e a URL/autenticação atualizadas no provedor, a compra avulsa passou pelo navegador público: tokenização real, pagamento R$ 10,00, 100 créditos liberados. Recebimentos autenticados: charge.created, order.created, order.paid, charge.paid. Estorno realizado no sandbox; charge.refunded e order.canceled recebidos, cobrança reversed e carteira zero. Nenhuma assinatura criada nesta validação. Evidência: checkout-sandbox-validation.json.

## Expansão de pagamentos — 28/10/2026

46 testes passaram em PostgreSQL com race detector, nenhum ignorado; go vet e sintaxe dos quatro scripts passaram. Revisão independente sem achados importantes. Sandbox real: Pix, cartão em duas parcelas, assinatura semanal com primeiro ciclo e renovação antecipada apenas no registro descartável, atualização de cartão e cancelamento pelo portal. Quatro pagamentos estornados, carteira zero. Boleto não habilitado no provedor (HTTP 412), tela e contrato testados com simulação; opção desligada. Notificações verificadas em Postgres/transporte controlado, sem SaaS destinatário real. Precificação Sinapses mensal conferida em R$ 89,90, com oferta antiga arquivada e links antigos preservados. Evidência: payment-expansion-validation.json.

## Simuladores e recusas — 28/10/2026

63 testes passaram com PostgreSQL/race, nenhum ignorado; go vet e sintaxe JavaScript passaram. Recusas reais de Pix R$ 500,01 e cartão 0028 confirmadas, carteira zero e webhooks de falha autenticados. Casos de boleto por CEP e sequências de processamento/estorno/chargeback cobertos por contratos controlados. Pagamento a menor não credita e continua rechecando, inclusive após falha transitória; complemento integral credita uma vez. Histórico guarda motivo/código sanitizado. Correção de endereço no retry não reutiliza CEP antigo. Ver sandbox-simulators.md e simulator-validation.json.

## Nova rodada real solicitada — 28/10/2026

63 testes automatizados passaram. Dez checkouts reais cobriram os oito cartões documentados e os dois limites do Pix. O cartão 0036 terminou failed no provedor, divergindo do sucesso documentado; não recebeu créditos. Quatro CEPs de boleto retornaram HTTP 412. Cenário0093 preservou pagamento e um crédito após estorno recusado; histórico foi corrigido para identificar falha de operação posterior. Ver requested-tests.md e requested-tests-validation.json.

## Permanência nos detalhes da cobrança — 29/10/2026

Reproduzido no navegador: os detalhes eram substituídos pela lista após a atualização automática de 5 segundos. Corrigido com estado explícito da cobrança aberta e proteção contra respostas atrasadas. Teste scripts/check-charge-details.js passou: permanência após 6,2s, atualização manual dos detalhes, retorno explícito, descarte de resposta após navegação e retomada da atualização automática da lista. Sintaxe JS e build da API com arquivos embarcados passaram. Sem alterações em pagamentos ou banco de dados; testes financeiros não repetidos nesta correção.

## 29/10/2026 — Gestão de assinaturas e integrações

69 testes com PostgreSQL e race aprovados, nenhum ignorado. Pausa/retomada, troca entre períodos flexíveis, prorrateio, extrato monetário imutável e compensação idempotente dos créditos em estornos. Gestão de provedores/notificações por Bearer com replay criptografado. Painel validado por Playwright com escritas interceptadas; APIs locais e migração verificadas na instalação. Evidência: `subscription-lifecycle-validation.json` e `subscription-ui-validation.json`. Nenhuma nova operação financeira foi enviada ao provedor nesta entrega.

## Renovação natural — 29/10/2026

Criadas três assinaturas sandbox ativas, de 1, 5 e 7 dias, R$ 10 e 1 crédito por ciclo. Primeiras cobranças confirmadas diretamente no Pagar.me. Carteira dedicada: 3 créditos. Sem alterar relógio, âncora ou next_at. Próximas renovações em 30/10, 03/11 e 05/11 por volta de 13:54 (Cuiabá). Evidência em `natural-renewals-validation.json`; renovação futura ainda aguarda as datas.
