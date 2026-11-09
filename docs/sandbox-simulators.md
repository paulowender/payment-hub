# Simuladores e respostas financeiras do Pagar.me

Referências oficiais consultadas em 28/10/2026: [boleto](https://docs.pagar.me/docs/simulador-de-boleto), [Pix](https://docs.pagar.me/docs/simulador-pix), [cartão](https://docs.pagar.me/docs/simulador-de-cart%C3%A3o-de-cr%C3%A9dito). Não há regra de simulação local para forçar uma aprovação: o Hub sempre consulta o provedor.

## Pix

O simulador aprova valores de até R$ 500,00 após uma etapa pendente. Acima de R$ 500,00, falha. Na API, os valores são centavos: 50000 e 50001, respectivamente. O projeto já validou sucesso real de R$ 10,00 e recusa real de R$ 500,01; a recusa não alterou a carteira.

## Boleto e CEP do comprador

Estas regras da documentação se aplicam ao simulador gateway `simulator`; não habilitam boleto em uma conta sem esse produto.

| CEP em customer.address.zip_code | Cenário do provedor | Tratamento do Hub |
| --- | --- | --- |
| 01046010 | Pagamento a menor | Revisão manual, sem créditos; continua consultando o pedido para eventual complementação |
| 57400000 | Pagamento a maior | Créditos contratados uma única vez, com registro de pagamento a maior |
| 70070300 | Sem conciliação | Pendente; sem créditos |
| Demais CEPs | Pagamento integral | Liberação após confirmação do provedor |

O endereço é enviado ao criar o cliente no Pagar.me. Corrigir o CEP depois de uma tentativa frustrada muda a chave idempotente da criação do cliente, evitando reutilizar um cliente com endereço anterior. Nenhuma nova cobrança financeira é criada se a sessão já foi submetida.

Na conta usada neste projeto, a tentativa real retornou HTTP 412 com a mensagem de boleto desabilitado, antes de criar o pedido. A opção continua desligada. Os cenários por CEP foram cobertos por respostas de contrato controladas e testes financeiros em PostgreSQL, não por emissão real nessa conta.

## Cartões do simulador gateway

| Cartão | Sequência esperada |
| --- | --- |
| 4000000000000010 | Sucesso |
| 4000000000000028 | Recusa |
| 4000000000000036 | Processando, depois pago |
| 4000000000000044 | Processando, depois falha |
| 4000000000000077 | Pago; estorno passa por processamento e termina estornado |
| 4000000000000093 | Pago; tentativa de estorno passa por processamento e retorna ao pago |
| 4000000000000051 | Processando, depois cancelado |
| 4000000000000069 | Pago, depois chargeback |

As sequências foram verificadas com o gateway e o worker reais do Hub usando respostas controladas. Sucesso 0010 já foi validado no sandbox real; recusa 0028 também foi validada agora. Simulador PSP possui regras próprias, como CVV para recusa; use a documentação correspondente à conta. Não use cartões reais em testes.

## Histórico e notificações

O Hub mantém motivos resumidos, nunca PAN, CVV ou mensagens brutas do adquirente:

- `payment_declined:CODIGO`: código de recusa do adquirente ou gateway, quando disponível.
- `payment_declined`: recusa sem código seguro.
- `payment_underpaid`: pagamento insuficiente, não libera créditos.
- `payment_overpaid`: valor a maior; não aumenta os créditos da oferta.
- `payment_reversed`: estorno ou contestação.

Os motivos ficam no histórico da tentativa, no `last_error` da cobrança e no campo `reason` do evento financeiro. O checkout recebe apenas esse motivo resumido. HTTP 412 de configuração continua distinto de um pagamento recusado com pedido persistido. Falhas com pedido conhecido sempre consultam o mesmo pedido; não criam outra cobrança.

A tela de pagamento parcial continua atualizando. Se o provedor confirmar o pagamento integral depois, a liberação acontece uma vez. Falha temporária de conexão durante essa verificação preserva a elegibilidade para nova consulta. Pagamento confirmado não regride para pendente durante processamento de estorno; créditos só são revertidos após confirmação da reversão.

## Evidência

Recusas reais: Pix R$ 500,01, cartão 0028, status failed em ambos, carteira final zero. Webhooks autenticados charge.payment_failed e order.payment_failed correlacionados no histórico. Nenhum pagamento foi aprovado nessa validação de recusas, portanto não houve valor a estornar. Consulte simulator-validation.json.
