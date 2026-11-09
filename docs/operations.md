# Operação

## Antes de produção

1. Homologar cada conta com sandbox real: pagamento, recusa, SCA, timeout, renovação, estorno e chargeback. Os testes entregues usam PostgreSQL real e servidores HTTP locais, não contas dos provedores.
2. Colocar TLS e limites de tráfego no proxy de entrada. Restringir `/admin/*` a rede administrativa. A aplicação não tem rate limiting próprio; isso deve ficar no proxy.
3. Fornecer credenciais por secret de implantação, nunca commitá-las. A imagem roda como UID 10001; o arquivo montado precisa ser legível por esse usuário. Usar PostgreSQL gerenciado com TLS e conta restrita em produção. O Compose usa rede local privada e TLS desativado para desenvolvimento.
4. Configurar backups e testar restauração. Fazer backup antes de migrações. O schema atual inclui migrações 1 e 2; futuras versões devem introduzir migrações numeradas e compatíveis, não editar o schema inicial como estratégia de upgrade.
5. Criar alertas para `manual_review`, `past_due`, falhas do worker, fila atrasada, saldo negativo, erros de banco e idade do backup. A versão inicial registra erros via slog; métricas/prometheus não estão implementadas.
6. Validar consentimento off-session no produto. Frontend e tokenização são do SaaS integrador. Revisar retenção de dados e regras operacionais da empresa antes da abertura a terceiros.

Não há garantia de operação exatamente uma vez através da rede. A segurança depende da combinação de registro local persistente, chaves estáveis no provedor, limites de retentativa e reconciliação. Nunca gerar um novo charge_id para tentar resolver uma cobrança de resultado desconhecido.

## Configuração e rotação

As conexões são cadastradas pelo painel e consultadas do banco a cada operação. Todas as réplicas precisam do mesmo banco e da mesma ENCRYPTION_KEY. Exigir `environment: test|live`; Pagar.me em test permite no máximo 240s. O padrão de janela é 240s; em live, pode ser aumentado explicitamente até 82800s. Não redefinir a janela de cobranças desconhecidas para estender retentativas após o limite original do provedor.

A cobrança e a assinatura congelam um hash da credencial do provedor na criação. Se o segredo mudar, o worker coloca as cobranças afetadas em `manual_review`, antes de chamar uma possível conta diferente. Esse bloqueio também ocorre quando se rotaciona a chave mantendo a mesma conta: é conservador e exige validação administrativa. Não reutilizar um projeto para trocar de conta do provedor com assinaturas em andamento; preferir novo projeto.

Para rotação legítima da MESMA conta: pausar workers; confirmar no provedor o account/merchant ID das duas chaves; reconciliar resultados desconhecidos; fazer backup; cadastrar a nova credencial no painel e os hashes congelados das cobranças/assinaturas afetadas em uma transação administrativa; retomar workers. Nunca resetar `first_attempt`. Se não houver certeza de que é a mesma conta, não desbloquear a cobrança. O sistema não fornece API pública para essa alteração sensível.

A chave do projeto é diferente da credencial do provedor. Para rotacionar a chave da API do Hub, use `POST /admin/tenants/{id}/rotate-key` com o segredo administrativo e corpo `{}`; isso invalida a chave anterior para novas requisições. Requisições já autenticadas podem concluir. Distribua a nova chave ao backend do SaaS.

## Cobrança em revisão manual

1. Localizar `tenant_id`, `charge_id`, provedor, horário e `last_error`. O ID do Hub é a chave de idempotência e é enviado como `metadata.hub_charge_id`.
2. Buscar a cobrança diretamente na conta correta do provedor. Confirmar metadata, cliente, moeda e valor. Não confundir ID de pedido Pagar.me (`or_...`) com ID da transação (`ch_...`). O Hub usa o pedido em `provider_id` e mantém o ID da primeira transação em `provider_charge_id` para renovações.
3. Havendo pagamento/intent/pedido confirmado, registrar seu ID no banco e reativar somente a reconciliação (`status='pending'`, `next_at=now()`, limpar lease), mantendo `first_attempt`. Executar em transação com lock da linha, guardar evidência operacional e conferir que o ID não está vinculado a outra cobrança. O worker fará GET, validará valor e moeda e aplicará o saldo uma vez.
4. Sem evidência conclusiva, manter bloqueado. Só encerrar como falha após confirmar que não houve pagamento. Um novo pagamento deve ser uma nova decisão explícita do negócio, nunca retentativa automática da cobrança ambígua.

O painel mostra cobranças em revisão, mas não fornece tela de resolução manual nem endpoint para alterar estados manualmente. O operador deve saber usar transações SQL e guardar seu registro de intervenção. Não editar saldo ou ledger diretamente para corrigir pagamentos; usar a reconciliação e os lançamentos compensatórios.

## Consultas de monitoramento

```sql
SELECT status, count(*), min(created_at) AS oldest
FROM charges GROUP BY status;

SELECT tenant_id,id,status,last_error,first_attempt
FROM charges WHERE status='manual_review' ORDER BY created_at;

SELECT tenant_id,customer_id,balance FROM wallets WHERE balance<0;

SELECT tenant_id,id,now()-next_at AS delay FROM charges
WHERE status IN ('queued','pending','requires_action')
  AND next_at<now()-interval '5 minutes';

SELECT tenant_id,id FROM subscriptions WHERE status='past_due';
```

Pagamentos `paid` e falhas com ID conhecido continuam sendo consultados a cada 6h para detectar estorno/chargeback. Isso gera chamadas durante toda a retenção da cobrança. Planejar limites de API e futura política de janela de reconciliação de acordo com os contratos. O worker é deliberadamente pequeno; para grande volume, separar workers por partições e adicionar métricas antes de escalar.

## Regras de falha e recuperação

- Falha de conexão/5xx/429/4xx sem ID de pagamento: repetir a mesma chave/payload dentro da janela. Erros permanentes sem ID podem chegar a `manual_review`; sem heurística de classificação de mensagens específicas do provedor.
- Provedor retornou ID: consultas seguintes usam esse ID, não criam nova cobrança.
- Worker caiu: lease expira em 60s. Outra instância pode retomar; o token do lease impede resposta atrasada de sobrescrever o trabalho atual.
- Pagamento confirmado, commit local falhou: consultar novamente ou repetir a mesma criação dentro da janela. Ledger único impede duplicar créditos.
- Assinatura com pagamento falho/revertido: muda para `past_due`, sem novas faturas automáticas. Corrigir o meio no provedor e criar uma nova assinatura seguindo a política comercial do SaaS.
- Períodos perdidos por indisponibilidade são pulados após emitir uma única cobrança de retomada. Essa versão não calcula compensação retroativa.
- Estorno parcial ou chargeback reverte todos os créditos daquela cobrança. Se o cliente já consumiu, saldo negativo impede novos consumos; créditos posteriores compensam o débito.

No Pagar.me, a reconciliação usa o pedido, incluindo cobranças novas geradas em reprocessamento antifraude. Usa o ID da cobrança paga como origem da recorrência, não necessariamente o primeiro elemento da lista. Falhas são reconsultadas; uma aprovação tardia credita a carteira, mas a assinatura suspensa permanece past_due até intervenção comercial.

## Painel
URL local: http://localhost:18080/console. Bootstrap servido pelo próprio Go. Segredo administrativo dá acesso a todos os projetos; chave de projeto dá acesso apenas ao próprio projeto. Cookies HttpOnly/SameSiteStrict, sessões de 12h e escrita protegida por Origin. Em HTTPS, terminar TLS em proxy confiável e encaminhar X-Forwarded-Proto=https. Nunca expor /admin ao público sem restrições adicionais.
ENCRYPTION_KEY precisa de backup separado junto com os backups do banco. Não rotacionar apagando a chave antiga: o MVP ainda não tem ferramenta de recriptografia automática. As credenciais de provedor ficam cifradas no banco; acesso ao servidor que possui a chave mestra e ao banco permite decifrá-las.

## Webhook Pagar.me
Cadastrar URL HTTPS pública com rota /webhooks/pagarme/{tenant_id} e habilitar a autenticação com o mesmo usuário/senha salvos no painel do Hub. Eventos sugeridos: order.paid, order.payment_failed, order.canceled, charge.paid, charge.payment_failed, charge.refunded, charge.partial_canceled e chargeback.received. A entrega é deduplicada por event_id e desperta consulta ao provedor. O feed Eventos recebidos mostra ID/tipo/horário sem dados de cartão ou chave.
A instalação local não é diretamente acessível pelo Pagar.me. Enquanto não houver endereço HTTPS público, o polling mantém os estados atualizados e testes locais podem exercitar o receptor autenticado.

Configuração atual do webhook: habilitar grupos Cobrança e Pedido, mais chargeback.received no grupo Chargeback. charge.chargedback é legado descontinuado; não depender dele. Assinatura/Cartão podem ser desabilitados: a recorrência é do Hub e esta versão não gerencia o ciclo de vida dos cartões. chargeback.received antecipa a reconciliação dos pedidos pagos do projeto, inclusive quando não há order_id no envelope. Não concede nem reverte créditos diretamente pelo payload.
