# Payment Hub Implementation Plan

**Goal:** API executável de cobrança e créditos para vários SaaS.
**Architecture:** monólito Go + PostgreSQL, worker com leases e reconciliação.
**Spec:** architecture.md
**Tech Stack:** Go 1.26, pgx v5, PostgreSQL 17, Docker Compose.

Autorização: usuário delegou decisões e execução direta. Implementação nesta sessão, sem rodada adicional de aprovação.

## Arquivos e tarefas
- [x] core.go/core_test.go: calendário ancorado e validação; testes de fevereiro, ano bissexto e intervalos inválidos.
- [x] schema.sql/store.go: esquema com isolamento, fila, ledger imutável e idempotência; integração PostgreSQL com consumo concorrente, repetição e isolamento.
- [x] provider.go/provider_test.go: adaptadores HTTP Stripe/Pagar.me, contratos por servidor HTTP local, estados e erro ambíguo.
- [x] api.go: provisionamento, clientes, planos, assinaturas, cobranças, wallet e eventos com API documentada; testes de validação e autenticação.
- [x] worker.go: emitir ciclos com lock, cobrar com chave estável, reconciliar e creditar exatamente uma vez; teste de worker até saldo com provedor local.
- [x] cmd/server, Dockerfile, compose.yaml, exemplos e README: inicialização, migração, encerramento e instruções; go test -race ./..., go vet ./..., build e teste de fumaça real em PostgreSQL.

## Review focus
Falha depois do provedor aceitar pagamento; dois workers no mesmo ciclo; duas retiradas competindo pelo saldo; chave de outro projeto referenciando cliente; estorno após consumo de créditos. Cobrir com testes de integração e restrições de banco.

## Registro de execução
- Usuário delegou decisões e execução direta nesta sessão.
- Revisão independente identificou janela sandbox insegura; corrigida com padrão 240s, ambiente explícito e validação na inicialização.
- Testes reproduziram e corrigiram representação JSON no replay idempotente, avalanche de períodos após indisponibilidade e troca de conta/credencial após criação.
- Credenciais congeladas por hash; rotação exige validação administrativa documentada.
- Segunda revisão independente sem pendências relevantes de código.
- Sem implantação pública ou movimentação de dinheiro real.
