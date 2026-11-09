# Payment Hub — decisões e escopo

Plataforma própria de orquestração: dinheiro é processado por Stripe/Pagar.me; a plataforma mantém cobranças, recorrência e créditos de uso. Não é banco, adquirente ou carteira de dinheiro sacável.

## Arquitetura escolhida
Monólito modular Go, API HTTP JSON e worker no mesmo binário, PostgreSQL como registro e fila. Uma instalação atende vários projetos (tenants). Cada projeto tem chave independente, clientes, planos, assinaturas, cobranças e carteira separados. Credenciais dos provedores são cadastradas no painel e ficam criptografadas no banco com AES-256-GCM e binding por projeto/provedor. A chave mestra de implantação fica no ambiente do servidor. Pode-se implantar uma instalação por empresa quando necessário, sem alterar código.

Não há benefício automático de segurança em Go: a escolha reduz complexidade operacional e oferece concorrência e HTTP na biblioteca padrão. A segurança depende dos controles e da operação.

## Regras
- Valores monetários inteiros na menor unidade da moeda, nunca float. Inicialmente BRL e USD no Stripe; Pagar.me somente BRL.
- Créditos são inteiros, vinculados ao cliente e ao projeto; não se convertem automaticamente em dinheiro. Sem saque, transferência entre projetos ou vencimento nesta versão.
- Autenticação por Bearer com chaves aleatórias, hash SHA-256 no banco e comparação no banco. Provisionamento administrativo com segredo separado.
- Todas as referências entre recursos incluem tenant_id em chaves estrangeiras compostas. Uma chave de projeto nunca escolhe o próprio tenant_id.
- Idempotency-Key obrigatória em todas as escritas de negócio; reutilizar chave com outro conteúdo resulta em conflito. A gravação do recurso e da resposta ocorre na mesma transação.
- Cobrança registrada antes da chamada externa. Worker com lease e SKIP LOCKED. Falhas ambíguas repetem a MESMA chave do provedor e o mesmo payload; após a janela configurada, bloqueiam em manual_review para evitar expiração da janela do provedor.
- Estado do pagamento é consultado diretamente no provedor. Polling dispensa confiar em notificações externas e reconcilia pagamentos perdidos. O webhook Pagar.me autenticado com Basic Auth apenas acorda a reconciliação; nunca confirma pagamento pelo payload.
- Sucesso do pagamento e concessão de créditos são uma transação. Estorno parcial ou total/chargeback confirmado reverte todos os créditos concedidos por aquela cobrança; saldo pode ficar negativo se já consumidos. Consumo exige saldo suficiente.
- Recorrência própria: intervalos de 1–365 dias ou 1–120 meses. Âncora de calendário UTC preservada: 31/jan → último dia de fev → 31/mar. Uma fatura por assinatura e ciclo. Cobramos o primeiro ciclo no início (trial via start_at futuro). Depois de indisponibilidade, emitir somente uma cobrança e avançar ao próximo período futuro, sem cobrança retroativa automática dos ciclos perdidos. Cancelamento impede novos ciclos; não cancela cobrança já emitida. Próximo ciclo aguarda pagamento do anterior; falha exige nova assinatura após correção, sem dunning automático.
- Payment methods e clientes devem estar previamente criados no provedor e autorizados para cobrança recorrente; nunca receber PAN/CVV. SCA retorna requires_action e client_secret somente pela API autenticada, para resolução no SaaS.
- Eventos de negócio persistidos na mesma transação, disponíveis por cursor para os SaaS; consumidores guardam seu cursor e deduplicam por ID. Sem envio HTTP para URLs arbitrárias nesta versão.

## Entidades
Tenants → customers → wallets/ledger; tenants → plans → subscriptions → charges. Charges referenciam provider e referências tokenizadas, preço e créditos congelados na emissão. API idempotency e events são por tenant. Planos são imutáveis: alterar preço exige novo plano.

## Limites e operação
Esta entrega é um MVP funcional de backend, não certificação de produção. Com painel Bootstrap, sem onboarding público, prorrateio, impostos/notas fiscais, cupons, split, reembolso iniciado via API ou gestão de cartões. Estornos são iniciados no provedor e reconciliados aqui. Pix/boleto ficam para extensão dos adaptadores; a versão inicial cobra cartão salvo.

Antes de produção: homologar com contas sandbox reais, validar consentimento/mandatos e SCA no frontend, configurar TLS, backups com restauração ensaiada, restrição de rede no provisionamento admin, rotação de segredos, alertas manual_review e métricas de atraso. Rodar instâncias com banco compartilhado e mesma configuração. Usar ambientes e projetos separados para teste e produção. Os logs não registram credenciais ou respostas completas dos provedores.

## Referências consultadas em 27/10/2026
- https://docs.stripe.com/api/payment_intents/create
- https://docs.stripe.com/webhooks
- https://docs.pagar.me/reference/criar-pedido-2
- https://docs.pagar.me/docs/o-que-%C3%A9

A configuração exige environment=test ou live. Pagar.me em test rejeita janela maior que 240s; chave sk_test não pode usar live. Pagar.me documenta janela de idempotência de 24h (5min no sandbox). Por isso a configuração define um limite menor por conta: padrão conservador 240 segundos; produção permite configurar 23h explicitamente. Se a janela expirar sem ID conhecido, intervenção manual, nunca nova cobrança automática.

Stripe API fixada em 2025-06-30.basil. Pagar.me usa core/v5 e envia payment_origin.charge_id nas renovações, referenciando a primeira cobrança da assinatura. Revalidar contratos antes de atualizar versões.

## Painel e conexões — versão 2
Migração 002 cria provider_connections, console_sessions e webhook_receipts sem alterar registros financeiros existentes. Sessões duram 12h, usam token aleatório cujo hash fica no banco, cookie HttpOnly/SameSiteStrict e verificação de Origin e header próprio nas escritas. Rotacionar a chave do projeto revoga suas sessões. Operador de projeto acessa apenas seu tenant; administrador da instalação acessa todos. Chaves dos provedores nunca são devolvidas pela API do painel.
Bootstrap 5.3.8 é servido localmente. Atualizações automáticas mostram os 100 registros mais recentes no painel, enquanto a API de integração mantém cursores estáveis. Logs de webhook guardam somente ID, tipo, referência do pedido e horário; dados brutos do comprador não ficam no inbox.
