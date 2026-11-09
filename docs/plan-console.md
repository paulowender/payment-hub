# Painel e sandbox — plano de execução

Autorizado pelo usuário: instalar e executar localmente, testar Pagar.me sandbox e adicionar painel Bootstrap com credenciais por usuário/projeto.

1. Persistir conexões criptografadas em PostgreSQL com AES-256-GCM; chave mestra de implantação separada das credenciais do provedor. Migrar esquema sem apagar dados. Adaptar API e worker ao mesmo resolvedor de conexões.
2. Sessões autenticadas por chave administrativa ou de projeto, cookies HttpOnly/SameSite, CSRF e expiração. APIs do painel respeitam isolamento, nunca retornam segredos.
3. Webhook Pagar.me com Basic Auth, deduplicação durável e confirmação por GET no provedor antes de alterar créditos. Feed visível de recebimentos.
4. Painel Bootstrap servido pelo Go: projetos, conexões/teste, cobranças, assinaturas, clientes/planos, carteira e eventos em atualização automática.
5. Testes de isolamento, criptografia/tamper, autenticação, CSRF, webhook/replay e testes sandbox reais. Subir Compose local em porta livre, guardar acesso em arquivo privado, verificar no navegador e documentar resultados sanitizados.

## Execução
Todos os itens implementados. Fluxo real de sandbox validado até renovação e estorno. Revisão independente apontou e permitiu reproduzir uma condição de troca de projeto no frontend, corrigida com vínculo do editor ao tenant original. Instalação local usa porta 18080 e volume persistente. Endpoint de webhook público segue dependente de URL fornecida pelo operador.
