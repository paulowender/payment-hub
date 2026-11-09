---
name: Payment Hub
description: Operação de pagamentos por projeto
colors:
  primary: "#315fcc"
  background: "#101720"
  surface: "#18222e"
  foreground: "#e3eaf3"
  secondary: "#a6b5c8"
  success: "#7aceac"
typography:
  heading:
    fontFamily: "Segoe UI, Aptos, Arial, sans-serif"
    fontSize: "32px"
    fontWeight: 650
    letterSpacing: "-0.025em"
  body:
    fontFamily: "Segoe UI, Aptos, Arial, sans-serif"
    fontSize: "15px"
rounded:
  button: "7px"
  panel: "10px"
spacing:
  small: "8px"
  medium: "16px"
  large: "24px"
components:
  button-primary:
    backgroundColor: "{colors.primary}"
    textColor: "{colors.surface}"
    rounded: "{rounded.button}"
---
# Overview
Painel em modo Operate, com prioridade a leitura e ações. Bootstrap 5.3.8 serve como base local; identidade em azul e hierarquia de operação por projeto.

# Colors
Fundo frio claro, superfícies brancas, texto azul escuro. Azul indica ação e seleção; verde indica confirmação. Estado é descrito também em texto.

# Typography
Fonte do sistema, títulos de 32px/28px em celular, seções 22px e corpo 15px. Valores e horários com números tabulares.

# Layout
Sidebar de 232px no desktop. Em celular, uma coluna com navegação horizontal interna e tabelas roláveis. Tabelas sempre identificam projeto, cliente e estado.

# Components
Botões Bootstrap, formulários com rótulos, tabelas responsivas e diálogos nativos com Escape. Chaves em password, nunca retornadas após salvar.

# Do's and Don'ts
Mostrar dados reais e horários de atualização. Indicar carregamento e erro. Não apresentar estado pago antes da confirmação do provedor. Vincular cada formulário ao projeto de origem.

## Themes
Tema escuro é o padrão por preferência expressa do usuário. Bootstrap data-bs-theme com superfícies escuras em login, painel, formulários, tabelas e diálogos. Botão Tema claro/Tema escuro na lateral, com escolha salva no navegador e aplicada antes de carregar as folhas de estilo. Navegação lateral rolável em janelas de pouca altura.
