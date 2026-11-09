# Diagnóstico temporário do webhook

Ativado exclusivamente no projeto de sandbox, até 28/10/2026 00:33:25 -04. Máximo de 20 capturas por janela; depois da data/limite, a autenticação volta a ser obrigatória automaticamente. Não pode ser ativado para contas live.

Acesse Eventos recebidos → Diagnóstico do webhook → Ver payload e cabeçalhos. O modo registra inclusive requisições sem Basic Auth. Senhas, tokens, documentos, contatos e outros valores pessoais são ocultados; cabeçalhos desconhecidos guardam apenas presença/quantidade. O JSON é limitado por profundidade e quantidade de itens; não é uma cópia bruta do corpo.

Requisições não autenticadas recebem HTTP 200 com captured=true, authenticated=false e processed=false, sem inclusão nos recibos financeiros, sem reagendar cobranças e sem alterar saldo. Um status sent no Pagar.me durante essa janela significa captura, não homologação da autenticação.

A captura real validou o envelope id/type/data/account/created_at, Content-Type application/json e a ausência de Authorization na requisição recebida. Exemplo sanitizado em webhook-diagnostic-sample.json. A causa da ausência ainda requer investigar configuração do envio/intermediários; o formato do JSON está compatível com o receptor.

Os registros ficam em webhook_diagnostics e são visíveis somente em sessão autorizada do próprio projeto ou administrativa. Há índice por projeto e limite de 20 visualizações recentes. WEBHOOK_DIAGNOSTIC_TENANT e WEBHOOK_DIAGNOSTIC_UNTIL são parâmetros temporários da instalação, sem chaves de provedor. Para encerrar antes do prazo, removê-los de .env e recriar a API. A expiração não depende de reiniciar o serviço.
