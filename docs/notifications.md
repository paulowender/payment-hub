# Notificações de saída

Cada projeto pode cadastrar um destino HTTPS no console. Apenas eventos criados enquanto a configuração estiver ativa entram na fila. Desativar ou editar o destino afeta novos eventos; entregas existentes mantêm destino, conteúdo e chave originais. A chave fica criptografada e aparece somente na criação ou redefinição. Guarde a chave anterior até esvaziar a fila anterior à troca.

Rotas autenticadas do console, prefixadas por `/console/api/projects/{tenant}/notifications`:

- `GET config`: configuração sem a chave.
- `POST config`: JSON `{"url":"https://seu-dominio.com/webhook","enabled":true}`. A primeira resposta inclui `signing_secret`.
- `POST config/reset-secret`: gera e retorna uma nova `signing_secret`.
- `GET deliveries`: últimas 100 entregas com estado, tentativas, código HTTP e erro resumido.
- `POST deliveries/{event_id}/retry`: reenvia entregas com estado `failed` ou `dead`, exclusivo do administrador.

O POST de entrega contém `id`, `tenant_id`, `type`, `created_at`, `version: 1` e `data`. Identifique duplicatas pelo `id`, que permanece igual nos reenvios. A entrega tem garantia de pelo menos uma tentativa; interrupções após o receptor aceitar podem causar duplicatas. Implemente processamento idempotente no receptor.

Para autenticar a entrega, use o corpo HTTP original sem recodificar JSON. Calcule HMAC-SHA256 com a chave e a sequência `X-PaymentHub-Timestamp + "." + corpo`. Compare em tempo constante com o hexadecimal após `sha256=` em `X-PaymentHub-Signature`. Valide também uma janela curta de tempo (por exemplo, cinco minutos). `X-PaymentHub-Event-ID` repete o identificador do corpo.

Uma resposta 2xx conclui a entrega. Falhas, incluindo redirecionamentos, são repetidas até oito tentativas, com intervalos de 30, 60, 120, 240, 480, 960 e 1920 segundos. Depois, a entrega fica `dead` para análise e reenvio manual. Respostas do receptor não são armazenadas. Apenas HTTPS na porta padrão é aceito; endereços internos, locais e reservados são bloqueados também na resolução DNS da conexão. O receptor deve apresentar um certificado TLS válido e responder em até cinco segundos.
