# Checkout com a marca do projeto

## O que foi implementado

Cada projeto configura sua identidade (nome, descrição, logo HTTPS, cor principal e fundo) em **Checkout e marca**. O checkout não apresenta a marca Payment Hub. O contraste do texto e do botão acompanha as cores. A aparência inicial é escura e responsiva.

Uma oferta representa um produto, serviço ou pacote de créditos, com cobrança avulsa, ou aponta para um plano recorrente. O formulário permite criar o plano junto com a oferta, escolhendo preço e periodicidade; ao usar um plano existente, mostra seu preço e créditos de forma explícita. No painel, preços são digitados em reais (89,90); na API, continuam em centavos (8990). Valores em centavos, moeda BRL. Ofertas e planos são imutáveis: outro preço exige outra oferta. Sessões preservam uma fotografia da oferta e da marca; personalizações posteriores não alteram links já emitidos.

O backend do SaaS cria uma sessão com a chave de API do seu projeto. O link vale por 24 horas e aceita uma única compra. Cada referência de pedido é única por projeto; a mesma chave de idempotência e corpo retornam a mesma sessão. Uma sessão paga continua exibindo sua situação após o vencimento.

O cartão é tokenizado no navegador, diretamente no Pagar.me, com a chave pública. O servidor recebe somente token, identificação e endereço do comprador. Não recebe número, validade ou CVV. Cliente e cartão são cadastrados no Pagar.me e uma cobrança é enfileirada no worker existente. A tela acompanha a confirmação. Créditos só são liberados após verificação do pagamento no provedor. O comprador não pode alterar preço, créditos, projeto ou cliente da sessão.

Compras avulsas aceitam cartão, Pix e boleto conforme a configuração do projeto. Cartão permite de uma a doze parcelas, limitadas pelo valor mínimo por parcela, sem acréscimo ao comprador; as taxas contratadas com o provedor continuam a cargo do lojista. Pix mostra código copia e cola, QR Code e validade; boleto mostra linha digitável, link e vencimento. As instruções são obtidas do provedor e persistidas; não liberam créditos por si só.

Nas assinaturas, há autorização explícita da periodicidade e do valor; o consentimento fica registrado por data na sessão. Primeiro ciclo imediato; próximos ciclos usam o plano e o agendador existentes. Assinaturas usam cartão, uma parcela por ciclo. O formulário oferece mensal, semanal (7 dias), quinzenal (14 dias), anual, a cada X dias (1–365) ou X meses (1–120). Reenvios concorrentes não duplicam cobrança nem assinatura. Os eventos `checkout.submitted`, `charge.queued`, `subscription.created` e os eventos financeiros existentes permitem acompanhar a operação.

## Preparar o Pagar.me

1. Em **Conexões → Pagar.me**, mantenha a chave secreta cadastrada e informe a **chave pública** da mesma conta/ambiente (`pk_test_…` no sandbox).
2. Cadastre o domínio do checkout na lista de domínios autorizados do painel Pagar.me.
3. Em **Checkout e marca**, personalize a marca e cadastre uma oferta.
4. Cadastre o cliente em **Clientes e créditos** ou na API.
5. Gere um link e abra-o para pagar. No sandbox, utilize somente cartões de teste.

Com cartão habilitado e sem chave pública, o painel orienta a configuração e não gera sessões inutilizáveis. Projetos que aceitam apenas Pix/boleto não precisam de chave pública para criar links. A chave pública é visível por definição; a chave secreta continua criptografada e nunca integra a resposta pública.

Referências do provedor: [tokenização](https://docs.pagar.me/reference/tokeniza%C3%A7%C3%A3o-1), [criação de token](https://docs.pagar.me/reference/criar-token-cart%C3%A3o-1), [cartão do cliente](https://docs.pagar.me/reference/criar-cart%C3%A3o).

## API para integrar qualquer SaaS

Todas as operações `/v1` exigem `Authorization: Bearer CHAVE_DO_PROJETO`. Escritas exigem `Idempotency-Key` único por operação, além de `Content-Type: application/json`. A chave nunca deve ser incluída no frontend do SaaS.

Cadastrar cliente: `POST /v1/customers`

```json
{"external_id":"usuario_123"}
```

Cadastrar produto avulso: `POST /v1/offers`

```json
{"name":"Pacote de 100 créditos","description":"Créditos para utilizar no serviço","amount":1000,"currency":"brl","credits":100}
```

Cadastrar oferta recorrente: `POST /v1/offers` (valor e créditos são obtidos do plano).

```json
{"name":"Plano Pro","description":"Acesso mensal","plan_id":"ID_DO_PLANO"}
```

Criar oferta e plano flexível juntos: `POST /v1/offers`

```json
{"name":"Sinapses quinzenal","description":"Acesso a cada 14 dias","amount":8990,"currency":"brl","credits":50,"interval_unit":"day","interval_count":14}
```

O plano é criado na mesma transação. Para mensal, use `interval_unit: "month"`, `interval_count: 1`. Planos existentes mantêm seus próprios preços. `POST /v1/offers/{id}/archive` com `{}` impede novos links da oferta; links já emitidos conservam a fotografia original até expirar.

Criar link: `POST /v1/checkout-sessions`

```json
{"offer_id":"ID_DA_OFERTA","customer_id":"ID_DO_CLIENTE","reference":"pedido_123"}
```

Resposta:

```json
{"id":"TOKEN_ALEATORIO","path":"/checkout/TOKEN_ALEATORIO","url":"https://pagamentos.sualoja.com.br/checkout/TOKEN_ALEATORIO","expires_at":"2026-10-29T20:00:00Z"}
```

`url` usa o domínio configurado na marca ou a URL pública da instalação. Sem ambos, retorna caminho relativo. O SaaS pode combinar `path` com seu próprio domínio. Redirecione o comprador para esse endereço. Consulte a sessão por `GET /v1/checkout-sessions/{id}`: `charge_id` e `subscription_id` vinculam as entidades. Consulte o estado em `GET /v1/charges/{charge_id}` ou os eventos em `GET /v1/events?after=CURSOR`. Nunca libere acesso com base em uma mensagem enviada pelo navegador do comprador.

Listagens: `GET /v1/offers`, `GET /v1/checkout-sessions` (cursores `after`/`limit`, até 100). Marca: `GET /v1/branding`, `POST /v1/branding` com `name`, `description`, `logo_url`, `primary_color`, `background_color`, `checkout_base_url` opcional. Cores hexadecimais de seis dígitos; sem CSS/HTML arbitrário.

## Domínio próprio

Configurar uma URL na marca apenas muda os links gerados. DNS, HTTPS e o proxy precisam estar configurados pelo responsável pelo SaaS. Encaminhe `/checkout/*` sem remover o prefixo e sem autenticação do seu SaaS. Exemplo Caddy no mesmo host do Payment Hub:

```caddy
pagamentos.sualoja.com.br {
    @checkout path /checkout/* /portal/*
    handle @checkout {
        reverse_proxy 127.0.0.1:18080
    }
    handle {
        respond "Not found" 404
    }
}
```

Para embutir a experiência sob o domínio do próprio SaaS, inclua o mesmo bloco de rota no proxy existente. Se houver uma SPA, a rota do checkout deve preceder seu fallback. A chave secreta não deve ser adicionada ao proxy nem ao navegador. Iframe não é habilitado; utilize navegação direta para preservar a origem e a segurança da captura.

Os gateways incluídos na instalação só expõem as rotas exatas do checkout e do webhook. Console, administração e `/v1` continuam bloqueados no acesso público. O túnel de desenvolvimento é temporário e não substitui um domínio de produção.

## Limites e recuperação

Esta entrega implementa Pagar.me, BRL, uma oferta por compra, cartão parcelado, Pix e boleto para compras avulsas e cartão por ciclo nas assinaturas. Checkout Stripe, cupom, carrinho, juros ao comprador e configuração automática de domínio não foram implementados. A API de cobranças Stripe existente continua disponível. Boleto exige habilitação na conta do provedor; a conta sandbox usada neste projeto devolveu HTTP 412 (boleto desabilitado), por isso a opção está desativada neste projeto.

Até dez tentativas de preparação de pagamento por link. Se houver falha ao cadastrar o cartão no provedor, o comprador precisa informar o cartão novamente, pois tokens são de uso único. Nenhuma cobrança financeira é criada antes da conclusão desse cadastro. Após enfileirar, um pagamento recusado exige nova sessão, com nova referência de compra; não reutilize o link para tentar outra compra.

Não registrar os links completos em ferramentas de análise: o token dá acesso à sessão. A resposta pública não mostra nome, documento, telefone ou e-mail do comprador. Não há rastreadores no checkout. Logo externo usa URL HTTPS e política sem referenciador.

Webhooks autenticados aceleram a atualização; a verificação periódica do worker continua funcionando enquanto a configuração do remetente estiver pendente. HTTP 200 do modo de diagnóstico não significa confirmação de pagamento.

## Validação desta entrega

Testes PostgreSQL/race cobrem preço no servidor, isolamento entre projetos, idempotência da sessão, snapshot da marca, autorização recorrente com registro de data, concorrência, crédito único após confirmação pelo worker, expiração, limite de tentativas, falha do provedor, rejeição de cartão bruto e validação de cores/URLs. Teste de navegador com provedor simulado verificou envio direto do cartão ao processador e envio de apenas token ao Hub; responsividade validada a 390 e 1440 pixels. Rotas públicas verificadas sem exposição do console/admin/API.

**Validado no sandbox real:** tokenização direta pelo navegador, compra avulsa de R$ 10,00, confirmação do pagamento, liberação de 100 créditos, webhooks autenticados, estorno integral e reversão do saldo para zero. Evidência em `checkout-sandbox-validation.json`. Recorrência foi coberta por testes automatizados; não foi feita outra compra recorrente real nesta etapa. As imagens iniciais são demonstrações; `checkout-sandbox-confirmado.png` registra a confirmação real do sandbox.

## Configurações por projeto

Em **Checkout e marca → Configurar pagamentos e portal** escolha os meios e limites. API autenticada: `GET /v1/checkout-settings`, `POST /v1/checkout-settings` com todos os campos:

```json
{"card":true,"pix":true,"boleto":false,"max_installments":12,"min_installment":500,"pix_expires":1800,"boleto_due_days":3,"buyer_cancel":true,"buyer_update_card":true}
```

Ao menos um meio deve estar ativo; Pix de 300 a 86400 segundos, boleto de 1 a 30 dias, mínimo por parcela de 100 a 99999999 centavos. Projetos sem configuração usam cartão, uma parcela, mínimo de R$ 5,00, Pix 30 minutos, boleto três dias e ambas as permissões do portal. As condições de pagamento são fotografadas em cada sessão; alterar configuração afeta novos links. Permissões de cancelamento/troca de cartão são consultadas em cada ação do portal.

No POST público `/checkout/{token}/pay`, envie `method` (`card`, `pix`, `boleto`), `installments` (1 por padrão), `buyer` e, somente para cartão, `card_token`. Pagamentos não cartão não aceitam token ou parcelas maiores que um. Em `/data`, `instructions` contém `pix_code`, `qr_url`, `boleto_line`, `boleto_url`, `expires_at` quando disponibilizados pelo provedor. Status pending conserva as instruções, inclusive depois de recarregar a página.

Veja também [portal do comprador](portal.md), [notificações para SaaS](notifications.md) e [histórico e validação](payment-expansion.md).
