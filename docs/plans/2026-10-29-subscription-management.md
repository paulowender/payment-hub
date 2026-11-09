# Gestão de assinaturas e integrações

Implementação autorizada pelo usuário em 29/10/2026. Reutiliza Go/PostgreSQL e a autenticação por projeto. Nenhuma alteração de acesso público ou credenciais reais é necessária.

## Regras decididas

- Pausa congela o tempo restante do ciclo. Retomada desloca a próxima cobrança e a âncora pelo tempo pausado. Cobranças emitidas continuam existindo. Pausa não elimina inadimplência.
- Troca tem prévia e confirmação. O período novo começa na confirmação do pagamento, com abatimento proporcional do período pago não utilizado. Suporta períodos diferentes e moeda igual.
- Abatimento excedente fica como crédito financeiro da assinatura, separado dos créditos de uso. Será descontado de ciclos futuros. Créditos de uso são ajustados pelo saldo proporcional dos direitos anteriores, sem duplicação por trocas repetidas.
- Mudança que exige pagamento só altera o plano após confirmação pelo worker. Conflitos, cobranças não liquidadas e concorrência são bloqueados. Respostas tardias devem preservar a contabilidade e gerar revisão quando não puderem ser aplicadas com segurança.
- Provedores e notificações recebem rotas /v1 autenticadas pela chave do próprio projeto. Escritas têm idempotência. Segredos não voltam em consultas e qualquer replay que contenha segredo fica criptografado no banco.

## Tarefas e validação

1. Lifecycle: migração, pausa/retomada, prévia/troca, integração com agendador e reconciliação, testes Postgres.
2. Management: rotas de provedores e notificações com escopo/idempotência, testes de isolamento e segredos.
3. Painel: ações de pausa/retomada e prévia de troca com confirmação; apresentar saldos e alterações pendentes.
4. Documentação OpenAPI e guias; execução da suíte com Postgres e race, verificação do navegador, revisão independente e instalação local.

## Progresso

- Tarefas 1 e 2 em implementação por subtarefas independentes; arquivos compartilhados limitados aos respectivos pontos de roteamento.

- Concluído: lifecycle, gestão API, painel, auditoria monetária e documentação.
- Revisão encontrou e corrigiu estorno de créditos derivados, pagamentos tardios e preservação de cancelamento.
- Verificação final: 69 testes Go com PostgreSQL/race, nenhum ignorado; vet, OpenAPI, navegador com escritas interceptadas e rotas públicas aprovados.
