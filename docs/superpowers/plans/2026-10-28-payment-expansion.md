# Evolução de pagamentos e operação

Decisões delegadas: manter Go/Postgres/JS e a operação atual; não depender de domínio definitivo. Entregar primeiro Pagar.me; checkout Stripe é opcional nesta solicitação e permanece fora desta rodada para não misturar autenticação adicional de outro provedor com os fluxos brasileiros.

- [x] Configurações por projeto: cartão/Pix/boleto, até 12 parcelas sem acréscimo ao comprador, valor mínimo por parcela, expiração Pix, vencimento boleto; permissões do portal. Fotografia das condições na sessão; assinaturas apenas cartão à vista por ciclo.
- [x] Gateway e worker: emitir Pix/boleto de forma idempotente, persistir código/linha/links/validade seguros, exibir instruções enquanto pending sem liberar créditos. Parcelas no cartão sem multiplicar o total. Preservar pagamentos já existentes e tokenização direta.
- [x] Histórico: tentativas e erros sanitizados, mudanças de estado, webhooks correlacionados por pedido; leitura isolada por projeto e detalhe no painel.
- [x] Notificações: configuração privada HTTPS, segredo criptografado, assinatura HMAC, outbox transacional a partir dos eventos, tentativas/reenvio, proteção contra SSRF, visualização no painel.
- [x] Portal: link temporário criado pela API/operador, uma assinatura, histórico limitado, cancelamento de próximos ciclos, atualização de cartão tokenizado com consentimento e permissões por projeto. Nenhuma cobrança existente é refeita pela troca do cartão.
- [x] Verificação: testes de contrato e Postgres/race, concorrência e isolamento, testes do navegador, emissões sandbox disponíveis, checkout recorrente sandbox com cancelamento e estorno, documentação/API/rotas públicas/exportação.

Falhas importantes: preço e parcelas adulterados; carteiras creditadas antes do pagamento; expiração sem regressão de paid; notificação duplicada/timeout/URL privada; token de portal expirado/reutilizado entre contas; cancelamento versus agendador; troca de cartão versus cobrança já emitida. Todas exigem verificação, sem armazenar PAN/CVV ou expor segredos.

Limitações verificadas: habilitação de boleto bloqueada pela conta Pagar.me; nenhum SaaS destinatário real configurado. Stripe opcional adiado.
