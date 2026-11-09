# Product
<!-- impeccable:product-schema 1 -->
## Platform
web
## Stack
Decisões técnicas delegadas pelo usuário. Go e PostgreSQL existentes; Bootstrap e JavaScript nativo no painel, conforme preferência expressa por Bootstrap ou shadcn/Tailwind.
## Users
Operadores dos próprios SaaS e responsáveis por cada projeto integrado.
## Product Purpose
Centralizar cobranças e créditos de vários projetos, com provedores independentes. Permitir cadastrar a chave do próprio provedor e verificar dados recebidos durante desenvolvimento.
## Capabilities and Constraints
Credenciais de provedor por projeto, criptografadas no banco; tela de conexão, cobranças, assinaturas e eventos. Sandbox Pagar.me autorizado para testes. Sem expor credenciais no navegador após salvar. Não armazenar dados brutos de cartão no Hub.
## Operating Context
Desenvolvimento local em /home/wender/Projetos/WENDERTECH/payment-hub. Idioma português. Acesso por chave administrativa ou chave individual de projeto.

## Checkout
Checkout transparente por projeto, marca e cores configuráveis, ofertas avulsas ou planos, sessões de 24 horas criadas por API/painel e cartão tokenizado diretamente no Pagar.me. Domínio próprio via proxy HTTPS; sem processamento de cartão bruto no Hub.

## Operação ampliada
Meios e parcelas por projeto, ofertas com plano criado na mesma operação e preço em reais no painel, histórico de tentativas, notificações SaaS assinadas com fila e reenvio, portal escopado de assinaturas. Pix/boleto são avulsos; renovação automática continua por cartão.
