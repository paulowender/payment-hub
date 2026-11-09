# Rodada de testes solicitada — 28/10/2026

63 testes automatizados passaram em PostgreSQL com race detector, nenhum ignorado. Go vet, sintaxe JavaScript, rotas públicas do gateway direto/túnel e login do painel passaram.

Foram submetidos dez checkouts reais ao sandbox, com cliente sintético separado. As ofertas de teste foram arquivadas para impedir novos links. Não foi usada nenhuma assinatura ou carteira de cliente real.

| Caso | Resultado observado |
| --- | --- |
| Cartão 0010 | Pago; estorno confirmado |
| Cartão 0028 | Recusado, código 01; sem créditos |
| Cartão 0036 | Processou e terminou failed, código 00; divergência da documentação, que prevê sucesso. Sem créditos |
| Cartão 0044 | Processou e falhou; sem créditos |
| Cartão 0051 | Cancelado/revertido; sem créditos |
| Cartão 0069 | Chargeback/revertido; crédito revertido |
| Cartão 0077 | Pago; estorno passou por processamento e foi confirmado |
| Cartão 0093 | Pago; operação posterior de estorno recusada. Pagamento original e seu crédito preservados |
| Pix R$ 500,00 | Pago; estorno confirmado |
| Pix R$ 500,01 | Recusado, código 500; sem créditos |

A tentativa de estorno do 0093 mostrou uma descrição incorreta no histórico: o retorno failed de uma operação posterior aparecia como recusa do pagamento original. Isso foi corrigido e testado; agora aparece `subsequent_operation_failed:00`, preservando o pagamento original. Registros anteriores à correção permanecem no histórico de auditoria. Retorno pending de uma operação posterior também recebe descrição específica, sem rebaixar a confirmação anterior.

O cliente sintético terminou com um crédito, correspondente ao pagamento preservado no cenário 0093, que recusa estorno por definição. Não há recorrência automática nem cliente real envolvido nesse saldo. Não forçamos estorno ou saldo zero para mascarar esse resultado esperado.

## Boleto

Foram criados quatro clientes no provedor, com os CEPs 01046010, 57400000, 70070300 e 01001000, e cobranças sandbox isoladas enviadas pelo worker. Como o boleto está desabilitado para novos links do projeto, estes testes foram fixtures explícitas do worker, sem alterar as configurações do checkout. Todos retornaram HTTP 412 e ficaram em revisão manual, sem pedido financeiro emitido. Isso confirma a restrição da conta; não valida nativamente pagamento parcial, excedente ou sem conciliação. Esses três fluxos seguem cobertos pelos testes controlados documentados em sandbox-simulators.md.

## Evidência

`requested-tests-validation.json` contém pedidos do provedor, estados das tentativas, tipos de webhooks autenticados e saldo final. `teste-estorno-negado-historico.png` mostra a descrição corrigida no painel. Nenhum PAN, CVV, segredo ou token de sessão é incluído no relatório.
