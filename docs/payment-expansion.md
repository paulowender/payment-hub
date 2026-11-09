# Pagamentos, histórico e integração SaaS

## Onde usar

- **Checkout e marca:** meios aceitos, parcelas sem acréscimo, mínimo por parcela, expiração Pix, vencimento boleto e permissões do portal. Configure antes de gerar novos links.
- **Cadastrar oferta:** escolha pagamento único ou assinatura. Para nova assinatura, defina preço na moeda selecionada, créditos e periodicidade. Preço `89,90` no painel corresponde a `8990` na API. Para plano já cadastrado, preço e créditos são exibidos e herdados; não há campos vazios bloqueados.
- **Cobranças → Ver detalhes:** até 100 resultados de tentativas/consultas, códigos de erro resumidos, mudanças de estado e webhooks autenticados correlacionados. `GET /v1/charges/{id}/history` exige a chave do projeto. Histórico de tentativas começa nesta versão; eventos/webhooks anteriores continuam disponíveis.
- **Notificações aos sistemas:** configure HTTPS e ative o envio; guarde o segredo mostrado uma única vez. Valide HMAC no SaaS destinatário, responda 2xx, deduplique por evento. Até oito tentativas; reenvio manual pelo administrador. Nenhum destinatário real foi cadastrado automaticamente.
- **Assinaturas → Portal do comprador:** link pessoal de 24 horas com consulta, cancelamento dos próximos ciclos e atualização de cartão. Gerar outro link revoga o anterior; entregue apenas ao comprador autenticado.

## Flexibilidade dos períodos

Semanal = 7 dias, quinzenal = 14 dias, mensal = um mês de calendário, anual = 12 meses. Campos personalizados aceitam de 1 a 365 dias ou de 1 a 120 meses. Meses preservam o dia âncora quando possível; dias são contados por calendário. O agendador pula períodos perdidos após interrupção para evitar uma sequência de cobranças acumuladas.

As ofertas são imutáveis e podem ser arquivadas. Sessões guardam preço, marca e condições escolhidas na emissão. Corrigir um preço exige criar uma oferta e gerar um novo link; links antigos não mudam silenciosamente.

## Sinapses

A oferta original estava vinculada ao plano de teste Sandbox diário de R$ 10,00. Foi arquivada para novos links. Foi criada **Plano Sinapses · Mensal**, R$ 89,90 por mês, preservando os 50 créditos que a oferta anterior herdava. O checkout foi aberto e o preço e período foram conferidos. Nenhuma compra foi feita nessa oferta corrigida. Para outro período ou quantidade de créditos, crie a oferta correspondente. Links antigos mantêm os R$ 10,00 e o período da sua fotografia; use a nova oferta para novos links.

## Verificação em 28/10/2026

- Sandbox real via navegador HTTPS: Pix (aprovação automática do simulador), cartão em duas parcelas, primeiro ciclo de assinatura semanal, atualização de cartão, renovação antecipada da assinatura descartável e cancelamento pelo portal.
- Quatro pagamentos sandbox estornados; carteira da validação voltou a zero e assinatura de teste está cancelada.
- Boleto: contrato e tela testados com provedor simulado. A conta real de sandbox respondeu **HTTP 412 — The boleto payment is disabled**. Opção desligada neste projeto até habilitação na conta Pagar.me. Não foi emitido boleto real nesta conta.
- Notificações: testes Postgres comprovam outbox transacional, assinatura, isolamento, repetição com ID estável, segredo criptografado e SSRF. Não foi testado o receptor de um SaaS real, pois não há URL de destino cadastrada.
- Testes incluem adulteração de preço/parcelas, intervalos flexíveis, conservação dos links arquivados, isolamento do histórico, permissões e expiração do portal, novos cartões com nova origem de recorrência e erros permanentes do provedor.

## Limites conhecidos

Checkout Stripe ficou fora desta rodada; sua API de cobranças existente continua disponível. Assinaturas no checkout renovam por cartão, uma parcela por ciclo; Pix/boleto não são débitos automáticos. Parcelamento preserva o total ao comprador, sem calcular juros; custos do provedor são definidos no contrato do lojista. Túnel permanece temporário e domínio definitivo não foi configurado.

Leia os protocolos em [checkout](checkout.md), [portal](portal.md) e [notificações](notifications.md). Não distribua tokens de checkout/portal em páginas públicas ou rastreadores. Não envie número/CVV de cartão ao Hub.
