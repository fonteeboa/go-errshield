# go-errshield

![Go](https://img.shields.io/badge/go-1.27%2B-blue)
![License](https://img.shields.io/badge/license-MIT-green)
![CI](https://github.com/fonteeboa/go-errshield/actions/workflows/ci.yml/badge.svg)

Traduz erros de driver de banco (Postgres, MySQL, MongoDB, OpenSearch) para
mensagens seguras para o cliente, logando o erro original com um trace ID
para rastreio.

Requer **Go 1.27+**.

Módulo: `github.com/fonteeboa/go-errshield`

Princípio: **default-deny, com mensagens precisas**. Cada fonte tem seu próprio
allowlist de códigos. Só código explicitamente mapeado recebe mensagem
específica. Qualquer código fora do mapa cai no genérico `internal server
error`.

A mensagem é escrita à mão e **nunca** derivada da mensagem do driver. É isso que
a mantém segura: ela pode ser específica à vontade, porque não vem de lugar
nenhum que o banco controle.

A distinção que governa o allowlist:

| Seguro dizer | Não seguro dizer |
|---|---|
| *que tipo* de falha — "coluna não existe", "chave duplicada" | *qual* objeto — `users_email_key`, `secret_orders` |
| que o serviço está indisponível | `db.internal.prod.svc:5432` |
| que faltou um campo obrigatório | o valor que o cliente mandou |

Ou seja: **preciso sobre a categoria, omisso sobre o objeto**. Um cliente pode
ler "duplicate key value" tranquilamente — é sobre a requisição dele. Ele não pode
ler `users_email_key`, que é sobre seu schema.

O que você recebe na API é sempre um `Code` estável e uma mensagem genérica,
mais o `trace_id` para correlacionar. O detalhe real vai inteiro para o log —
constraint, tabela, campo, host, e o seu próprio contexto de encapsulamento —
mas nunca para a resposta.

## Instalação por fonte

Cada fonte é um **subpacote separado**, então você baixa apenas o driver que
usa. O pacote raiz (`go-errshield`) não importa nenhum driver.

```bash
# só Postgres (traz pgx e lib/pq)
go get github.com/fonteeboa/go-errshield/postgres

# só MySQL
go get github.com/fonteeboa/go-errshield/mysql

# só MongoDB
go get github.com/fonteeboa/go-errshield/mongo

# só OpenSearch (sem dependência de driver)
go get github.com/fonteeboa/go-errshield/opensearch

# sentinels do database/sql (recomendado junto com qualquer fonte)
go get github.com/fonteeboa/go-errshield/sqlerr

# várias fontes ao mesmo tempo
go get github.com/fonteeboa/go-errshield/postgres github.com/fonteeboa/go-errshield/mongo
```

Você normalmente já tem o driver no seu `go.mod`, então na prática basta
importar o subpacote que usa e rodar `go mod tidy`.

| Subpacote | Dependências que traz | Chave do allowlist |
|---|---|---|
| `go-errshield/postgres` | `pgx/v5`, `lib/pq` | SQLSTATE (`"23505"`) |
| `go-errshield/mysql` | `go-sql-driver/mysql` | número (`"1062"`) |
| `go-errshield/mongo` | `mongo-driver` | número (`"11000"`) |
| `go-errshield/opensearch` | nenhuma | tipo de exceção (`"version_conflict_engine_exception"`) |
| `go-errshield/sqlerr` | nenhuma | nome do sentinel (`"sql.ErrNoRows"`) |
| `go-errshield` (raiz) | nenhuma | — |

Cada banco usa um formato de código diferente, e nenhum é compartilhado: MySQL
`1062` e Mongo `1062` (se existisse) seriam coisas diferentes. Por isso cada
fonte tem o seu próprio `Registry`, com o namespace da fonte no log.

## Uso

### Postgres (pgx ou lib/pq)

```go
import "github.com/fonteeboa/go-errshield/postgres"

func (h *Handler) CreateJob(c *gin.Context) {
	traceID := requestid.FromContext(c)

	_, err := h.db.Exec(ctx, insertQuery, args...)
	if err != nil {
		safe := postgres.Translate(ctx, err, traceID)
		c.JSON(safe.HTTPStatus(), safe) // {"code":"DUPLICATE_ITEM","error":"cannot duplicate item","trace_id":"..."}
		return
	}
}
```

`postgres.Translate` usa `errors.As`, então funciona tanto com pgx quanto com
lib/pq, e atravessa `fmt.Errorf("...: %w", err)`.

### MySQL

```go
import "github.com/fonteeboa/go-errshield/mysql"

safe := mysql.Translate(ctx, err, traceID)
c.JSON(safe.HTTPStatus(), safe)
```

### MongoDB

```go
import "github.com/fonteeboa/go-errshield/mongo"

safe := mongo.Translate(ctx, err, traceID)
c.JSON(safe.HTTPStatus(), safe)
```

### OpenSearch

OpenSearch **não** passa por `Translate`. O client (`opensearch-go`) não te
devolve um `error` do Go tipado para falha de API — ele devolve uma `*Response`
HTTP cujo corpo você lê e faz parse você mesmo. Por isso a função deste pacote
recebe os bytes crus do body em vez de um tipo do client (funciona igual em
v2/v3/v4, já que o formato de fio do servidor não muda entre versões):

```go
import (
	"io"
	"github.com/fonteeboa/go-errshield/opensearch"
)

res, err := client.Index(indexName, body)
if err != nil {
	// erro de transporte/rede — não é isto que TranslateResponse trata
	c.JSON(http.StatusInternalServerError, gin.H{"error": goerrshield.DefaultMessage})
	return
}
defer res.Body.Close()

if res.IsError() {
	raw, _ := io.ReadAll(io.LimitReader(res.Body, opensearch.MaxBodySize+1))
	safe := opensearch.TranslateResponse(ctx, raw, res.StatusCode, traceID)
	c.JSON(safe.HTTPStatus(), safe)
	return
}
```

OpenSearch também não é consistente no próprio formato: a maioria dos
endpoints manda `{"error":{"type":"...","reason":"..."},"status":N}`, mas
alguns (ex. 405 Method Not Allowed) mandam `{"error":"string simples",...}`.
`TranslateResponse` trata os dois; o segundo formato cai sempre em
`internal server error` porque não tem um `type` estruturado pra mapear.

### `database/sql` sentinels (`sqlerr`)

Sentinels de `database/sql`, `database/sql/driver` e `context` **não são**
erros de driver, então nenhum extrator por fonte consegue casá-los. Sem o
`sqlerr` eles somem no genérico — e `sql.ErrNoRows`, que é o erro mais comum
que uma rota de Go encontra, viraria `internal server error` em vez de um 404.

```go
import "github.com/fonteeboa/go-errshield/sqlerr"

switch {
case postgres.Matches(err):
	safe = postgres.Translate(ctx, err, traceID)
default:
	safe = sqlerr.Translate(ctx, err, traceID)
}
```

| Sentinel | Code |
|---|---|
| `sql.ErrNoRows` | `NOT_FOUND` |
| `sql.ErrConnDone` | `UNAVAILABLE` |
| `driver.ErrBadConn` | `UNAVAILABLE` |
| `context.DeadlineExceeded` | `TIMEOUT` |
| `context.Canceled` | `INTERNAL_ERROR` |

`sqlerr.Translate` é um bom `default`: trata os sentinels e cai no genérico para
o resto. Os dois últimos logam em `WARN`, não `ERROR` — um cliente
desconectando não deve acordar ninguém.

### Usando várias fontes

`Matches` deixa você tentar cada fonte que o serviço usa. Só entram no `switch`
os subpacotes que você importou — e as dependências deles:

```go
import (
	"github.com/fonteeboa/go-errshield"
	"github.com/fonteeboa/go-errshield/mongo"
	"github.com/fonteeboa/go-errshield/postgres"
	"github.com/fonteeboa/go-errshield/sqlerr"
)

func translateAny(ctx context.Context, err error, traceID string) *goerrshield.SafeError {
	switch {
	case postgres.Matches(err):
		return postgres.Translate(ctx, err, traceID)
	case mongo.Matches(err):
		return mongo.Translate(ctx, err, traceID)
	default:
		return sqlerr.Translate(ctx, err, traceID)
	}
}
```

PG `23505` e Mongo `11000` são códigos diferentes mas os dois viram
`DUPLICATE_ITEM`, então a rota não precisa saber qual banco falhou.

### Fluxo completo

O que a API devolve e o que você vê no log, lado a lado:

```
=== API responses (what the client sees) ===
  DUPLICATE_ITEM         "cannot duplicate item"        trace_id=req-001
  NOT_FOUND              "resource not found"           trace_id=req-002
  UNAVAILABLE            "service temporarily unavailable" trace_id=req-003

=== log records (what you see) ===
WARN dberr: db error translated trace_id=req-001 source=postgres code=23505
  error="repo.InsertUser(email=***): duplicate key value violates unique constraint \"users_email_key\" (SQLSTATE 23505)" known=true
WARN dberr: db error translated trace_id=req-002 source=sql code=sql.ErrNoRows
  error="repo.FindUser: sql: no rows in result set" known=true
WARN dberr: db error translated trace_id=req-003 source=postgres code=pgconn.ConnectError
  error="db.Connect: failed to connect to `host=db.internal.prod.svc user=app database=app`" known=true
```

Note como o hostname interno e o nome do banco ficam no log mas não na
resposta, e como o `trace_id` amarra os dois lados.

### Falhas do pgx sem SQLSTATE

O pgx não reporta falha de conexão como `PgError`. São tipos próprios, que o
`postgres` trata em separado:

| Erro | Code |
|---|---|
| `*pgconn.ConnectError` | `UNAVAILABLE` |
| `net.Error` com `Timeout()` | `TIMEOUT` |

Sem isso, a classe 08 inteira do allowlist seria inalcançável via pgx — que é
exatamente onde ela importa.

### Só a string

```go
c.JSON(http.StatusConflict, gin.H{"error": postgres.Message(ctx, err, traceID)})
```

## Logging

Por padrão `go-errshield` loga no `slog.Default()`:

- `WARN` — código conhecido, translated com sucesso
- `ERROR` — código desconhecido (sinal de que falta entrada no allowlist),
  tipo de driver não reconhecido, ou body de OpenSearch não parseável

O log registra o **erro original inteiro**, incluindo o seu próprio contexto de
encapsulamento:

```
error="repo.InsertUser(email=admin@example.com): duplicate key value violates unique constraint \"users_email_key\" (SQLSTATE 23505)"
```

Esse contexto é o que diz qual chamada falhou — `repo.InsertUser` — e é o
motivo de a lib logar a cadeia inteira em vez de só a mensagem do driver.

### Redigindo o que você mesmo escreveu

O texto que **você** adiciona no `fmt.Errorf` costuma carregar o valor que é
sensível, e ele vai para o log junto:

```go
// seu repositório, em todo call site
return "", fmt.Errorf("repo.InsertUser(email=%s): %w", email, err)
```

Isso é uma escolha sua, e pode ser exatamente o que você não quer registrar.
`Redact` deixa você manter o contexto e descartar o valor:

```go
// no setup da aplicação, antes de servir tráfego
postgres.Registry().Redact("admin@example.com")
```

Ou, para valores que você não consegue enumerar, use marcadores estruturais:

```go
mongo.Registry().Redact("token=")
```

Cada marcador é substituído por `***`. As considerações:

- É **baseado em substring**, não estrutural. A lib não sabe que um endereço de
  e-mail é sensível e um nome de constraint não é — escolha marcadores que
  só apareçam no seu texto de encapsulamento.
- O marcador precisa casar com o **valor**, não com o rótulo. `"email="` remove
  o rótulo e deixa o endereço intacto; `"admin@example.com"` remove o endereço.
- Valores que o **driver** loga (constraint, tabela, coluna, index) não são
  redigidos: são o que você precisa para depurar, e nunca chegam a uma
  resposta.
- Não afeta a resposta de nenhuma forma. `Redact` só toca o log.
- Não é thread-safe. Chame no setup, não em request.

## Status HTTP

O mapeamento canônico `Code` → status vive em código, para que todos os
consumidores concordem. O problema que isso resolve: cada projeto reescrevia a
tabela e acabava mandando 500 num caso que era 404.

```go
safe := postgres.Translate(ctx, err, traceID)
c.JSON(safe.HTTPStatus(), safe)
```

Ou, se você precisar validar antes:

```go
if status, known := goerrshield.StatusFor(safe.Code); known {
	// sua política
}
```

`Code` desconhecido devolve **500** e `known=false` — degrada para erro em vez
de um status errado porém plausível. A lib nunca escreve a resposta e só importa
`net/http` para essas constantes: sobreponha por rota quando seu contrato
diferir.

| Code                    | HTTP status | Quando |
|-------------------------|-------------|--------|
| DUPLICATE_ITEM          | 409         | chave única violada |
| REFERENCE_NOT_FOUND     | 422         | FK aponta para registro inexistente |
| REFERENCE_IN_USE        | 409         | registro é referenciado por outros |
| MISSING_REQUIRED_FIELD  | 400         | coluna NOT NULL recebeu null |
| INVALID_VALUE           | 400         | CHECK violado |
| VALUE_TOO_LONG          | 400         | string truncada |
| VALUE_OUT_OF_RANGE      | 400         | numérico/date fora do intervalo |
| INVALID_INPUT           | 400         | formato inválido |
| COLUMN_NOT_FOUND        | 400         | coluna não existe (ver nota) |
| NOT_FOUND               | 404         | registro não encontrado (`sql.ErrNoRows`) |
| CONCURRENT_UPDATE       | 409         | serialização falhou, stepped down |
| DEADLOCK                | 409         | deadlock detectado |
| LOCK_TIMEOUT            | 504         | não conseguiu lock |
| TIMEOUT                 | 504         | timeout de query |
| TOO_MANY_CONNECTIONS    | 503         | pool esgotado |
| UNAVAILABLE             | 503         | banco inacessível |
| AUTH_FAILED             | 401         | falha de autenticação |
| PERMISSION_DENIED       | 403         | privilégio insuficiente |
| METHOD_NOT_ALLOWED      | 405         | método HTTP inválido |
| SCHEMA_ERROR            | 500         | tabela/função/coluna ambígua não existe |
| INTERNAL_ERROR          | 500         | qualquer coisa fora do allowlist |

### `SCHEMA_ERROR` não é `NOT_FOUND`

Essa distinção foi o ponto mais fácil de errar. Uma versão anterior mapeava
`42P01` (undefined_table) para `NOT_FOUND`, e o resultado era uma resposta 404
"resource not found" para o caso em que **a query implantada no seu serviço
referencia uma tabela que não existe**.

O cliente lê isso como "o registro que você pediu não existe" e mostra a tela
errada, enquanto o problema real — seu deploy está quebrado — fica invisível.
Para o operador, "tabela não encontrada" é o que diz que o serviço está caído;
para o cliente, "registro não encontrado" manda ele procurar um dado que está
ali.

Por isso schema tem código próprio e status 500:

| SQLSTATE | Situação | Code | HTTP |
|---|---|---|---|
| `42P01` | tabela não existe | `SCHEMA_ERROR` | 500 |
| `42883` | função não existe | `SCHEMA_ERROR` | 500 |
| `42P02` | coluna ambígua | `SCHEMA_ERROR` | 500 |
| `3D000` | database não existe | `SCHEMA_ERROR` | 500 |
| `42601` | erro de sintaxe | `INTERNAL_ERROR` | 500 |

`42601` fica no genérico de propósito: a mensagem do Postgres ecoa o statement
que falhou, e nenhum mapa sobrevive a isso com segurança.

### `COLUMN_NOT_FOUND`: a culpa pode ser sua

`42703` (undefined_column) é o caso em que **só a sua aplicação sabe a resposta**.
Pode ser o cliente mandando um nome de campo que sua API não aceita — erro dele,
400. Ou pode ser uma query hardcoded com um typo — erro seu, 500.

A lib não pode adivinhar, então o código é separado (`COLUMN_NOT_FOUND`, 400 por
padrão) e você decide na rota:

```go
if errors.Is(safe, goerrshield.ErrColumnNotFound) {
	// se sua API nunca interpola input do cliente em SQL,
	// isso é sempre bug seu: responda 500
	c.JSON(http.StatusInternalServerError, safe)
	return
}
```

`goerrshield.AllCodes()` devolve essa lista para ferramentas e testes.

## Branchando com `errors.Is`

Cada Code tem um erro sentinel, então a rota pode decidir em cima de `Code` sem
switch de string:

```go
safe := postgres.Translate(ctx, err, traceID)

switch {
case errors.Is(safe, goerrshield.ErrDuplicateItem):
	c.JSON(409, gin.H{"error": "that email is already registered"})
case errors.Is(safe, goerrshield.ErrMissingRequiredField):
	c.JSON(400, gin.H{"error": "name is required"})
default:
	c.JSON(safe.HTTPStatus(), safe)
}
```

O casamento é por `Code`, não por ponteiro: um erro recém-traduzido casa com o
sentinel mesmo sendo outra instância, e `Message` e `TraceID` são ignorados —
o `Code` é o contrato.

Para código que já trabalha com as constantes `Code`, há a forma string-keyed:

```go
if goerrshield.HasCode(err, goerrshield.CodeNotFound) {
	c.Status(http.StatusNotFound)
}
```

Um `SafeError` também sobrevive a re-encapsulamento, então um middleware pode
adicionar contexto sem perder a classificação:

```go
reported := fmt.Errorf("service.CreateJob: %w", safe)
errors.Is(reported, goerrshield.ErrTooManyConnections) // true
```

### O que um `SafeError` deliberadamente não faz

Ele **não** implementa `Unwrap`, então a cadeia de erros termina nele:

```go
errors.Unwrap(safe)  // nil
```

Expor o erro original pela cadeia deixaria qualquer chamador percorrer de volta
até a mensagem do driver e logar ou renderizar os nomes de constraint, tabela,
coluna e host — exatamente o que a lib existe para manter fora das respostas.
O original está no log, correlacionado pelo `TraceID`; não é alcançável a
partir do valor de resposta.

A consequência é que `errors.Is(safe, algumErroDoDriver)` é `false`, e isso é
de propósito: um `SafeError` carrega uma classificação, não uma causa.

Os sentinels são `*SafeError` com a mensagem canônica, e por isso respondem
`Error()` e `HTTPStatus()`. Trate-os como somente-leitura: são ponteiros de
pacote com campos exportados, e mutar um corromperia o sentinel do processo.

## Descobrindo códigos que faltam no allowlist

Um código não mapeado é a única evidência de que um banco de verdade produz algo
que a lib ainda não foi ensinada a descrever. Sem sinal, você descobre pelo bug
report do usuário.

```go
goerrshield.SetUnmappedHook(func(e goerrshield.UnmappedEvent) {
	unmappedTotal.WithLabelValues(e.Source, e.Code).Inc()
})
```

O hook dispara em código fora do allowlist, com `Source` e `Code` no espaço do
respectivo banco — `e.Code` é literalmente o valor que você adicionaria à
allowlist. Se nenhum extractor casou, `Source` vem como `"unknown"` e `Code` vem
vazio, o que distingue "código não mapeado" de "tipo de driver não reconhecido".

Na prática, rodando tráfego real:

```
=== how the API answered ===
  duplicate email        -> HTTP 409  DUPLICATE_ITEM       "cannot duplicate item"
  duplicate column       -> HTTP 500  INTERNAL_ERROR       "internal server error"
  no rows                -> HTTP 404  NOT_FOUND            "resource not found"

=== what the unmapped hook reported ===
  postgres:42P07                     2 occurrence(s)
  postgres:42883                     1 occurrence(s)
```

`42P07` aparece três vezes num dia e ninguém reclamou; `42883` aparece uma vez
e pode ser bug de query. A tabela de frequências decide o que vale mapear —
sem ela, os dois são invisíveis.

Cuidados:

- Roda **síncrono** na goroutine que chamou `Translate`. Mantenha barato:
  contador, métrica, ou append num conjunto limitado. Nada de I/O de rede.
- Um hook que **panica** é recuperado e logado, para um cliente de métricas com
  defeito não derrubar a requisição.
- `e.Err` é o erro original, **sem redigir**. Não encaminhe para onde o cliente
  enxergue.
- Roda **além** do log em `ERROR`, não no lugar dele.

## Estendendo os allowlists

Adicione entradas no `registry` do arquivo da fonte correspondente só quando
você tiver decidido conscientemente que aquele código é seguro de descrever
externamente. Códigos que-authors recommendam **não** mapear, porque vazam
schema:

- Postgres: `42601` (syntax_error) fica no fallback genérico, porque a
  mensagem ecoa o statement
- MySQL: `1064` (syntax_error) pelo mesmo motivo
- Mongo: `86` (IndexKeySpecsConflict) fica no fallback genérico;
  `27`/`28`/`29`/`68` estão mapeados como `INVALID_VALUE` genérico, sem o
  nome do índice
- OpenSearch: não há uma lista oficial fechada de `type` — os valores usados
  aqui vêm dos tipos de exceção mais comuns observados na API REST.

Referências:
- Postgres: https://www.postgresql.org/docs/current/errcodes-appendix.html
- MySQL: https://dev.mysql.com/doc/mysql-errors/8.4/en/server-error-reference.html
- Mongo: https://github.com/mongodb/mongo/blob/master/src/mongo/base/error_codes.yml

## Precedência em erros com múltiplos códigos

Um único erro do MongoDB pode carregar vários write errors (é comum em
`BulkWriteException`). A resposta não pode depender da ordem em que o servidor
os reportou, então o `mongo` ranqueia os candidatos e devolve o mais acionável
para o cliente:

```
[validation(121), duplicate(11000)]  -> DUPLICATE_ITEM
[duplicate(11000), validation(121)]  -> DUPLICATE_ITEM
```

Códigos mapeados sempre vencem códigos não mapeados, e entre mapeados vence o
mais específico (`121` é catch-all e fica por último). O mesmo vale para o
write concern error, que participa como candidato.

## Testes

```bash
go test ./...
go test -race ./...
go test -cover ./...
```

Os testes rodam offline contra os structs reais dos drivers
(`pgconn.PgError`, `pq.Error`, `mysql.MySQLError`, `mongo.CommandError`,
`mongo.WriteException`, `mongo.BulkWriteException`) e cobrem as quatro
fontes, `errors.As` através de `fmt.Errorf("%w")`, os dois formatos de erro
do OpenSearch, e o fallback default-deny para código desconhecido e tipo de
erro desconhecido.

Além disso:

- **`leak_test.go` em cada fonte** — percorre o allowlist inteiro, injeta uma
  mensagem contendo constraint, tabela, coluna, collection, índice, host e
  porta, e afirma que nada disso aparece no `SafeError`. É o teste que segura a
  promessa central da lib contra uma entrada futura escrita descuidadamente.
- **Fuzz** em `opensearch` — `FuzzTranslateResponse` parseia bytes não
  confiáveis e mantém três invariantes: nunca retorna `nil`, nunca inventa um
  `Code` fora do allowlist, nunca vaza marcador sensível. `FuzzRegistryResolve`
  faz o mesmo pela resolução de código.
- **Guardas de drift** — no `mongo`, testes cruzam a tabela de precedência com
  o registry nos dois sentidos, então os dois não podem divergir em silêncio.
  No core, testes cruzam `AllCodes()` com a tabela de status HTTP, e com o bloco
  de constantes de código, nos dois sentidos.

## Várias políticas no mesmo processo

`SetLogger`, `SetUnmappedHook` e `Redact` são estado de processo. Isso é o
comportamento certo para a maioria dos programas, mas impede dois serviços
independentes no mesmo binário, um servidor multi-tenant com política por
cliente, ou testes paralelos com políticas diferentes.

Para esses casos, crie `Instance`s explícitos — sem estado global:

```go
billing := goerrshield.New(
	goerrshield.WithLogger(billingLogger),
	goerrshield.WithUnmappedHook(func(e goerrshield.UnmappedEvent) {
		billingUnmapped.WithLabelValues(e.Source, e.Code).Inc()
	}),
)
registry := goerrshield.NewRegistryWith(billing, "postgres", postgresCodes)
```

`Instance` é imutável depois de construído e seguro para uso concorrente, então
duas instâncias não se atrapalham. As funções de nível de pacote continuam
existindo e apontam para `goerrshield.Default`.

## Limite de tamanho no OpenSearch

Este é o único ponto da lib que processa bytes influenciados por terceiros —
o body de erro do OpenSearch carrega o documento que falhou ao ser parseado, e
um documento pode ser arbitrariamente grande.

`opensearch.MaxBodySize` (1 MiB) limita o quanto é parseado e logado. Acima
disso, o corpo não é interpretado nem registrado — o log reporta o **tamanho**,
não o conteúdo, e a resposta é o genérico. E limite a leitura desde o começo,
para nunca bufferizar mais que isso:

```go
raw, _ := io.ReadAll(io.LimitReader(res.Body, opensearch.MaxBodySize+1))
safe := opensearch.TranslateResponse(ctx, raw, res.StatusCode, traceID)
```

O `+1` é para que o overflow seja detectável.

## Limites e validação

A lib aplica limites defensivos com **valores padrão**. Todos são
configuráveis — o chamador decide, a lib só garante um teto.

### TraceID

O `traceID` normalmente vem de um header de requisição, ou seja, é
influenciado por quem chamou. Sem teto, uma requisição pode colocar megabytes
no log e na resposta.

```go
// Padrão: 256 caracteres
goerrshield.SetMaxTraceIDLength(128)  // global
postgres.Registry().SetMaxTraceIDLength(64)  // por registry
```

Truncamento, não rejeição: o traceID volta mais curto, e o log e a resposta
continuam correlacionáveis. Um valor de `0` ou menor desliga o truncamento —
só faça isso se a origem do traceID for confiável.

### Marcadores de redação

`Redact` é para setup, mas a lista cresce se chamada por request. A lib
mantém um teto de marcadores por registry.

```go
// Padrão: 1000 marcadores
goerrshield.SetMaxRedactMarkers(500)      // global
postgres.Registry().SetMaxMarkers(50) // por registry
```

### Status HTTP no OpenSearch

Um status fora da faixa aceita vira `500` no log, para não quebrar parsers de
sistemas de monitoramento.

```go
// Padrão: 100..599
opensearch.SetMinStatusCode(200)
opensearch.SetMaxStatusCode(499)
```

### Redação no hook

Por padrão o hook recebe o erro **original** — é assim que ele serve para
descobrir códigos que faltam no allowlist. Se o hook encaminha erros para um
sistema externo (métricas, monitoring), ative a redação:

```go
goerrshield.SetUnmappedHook(func(e goerrshield.UnmappedEvent) {
    // e.Err         -> original, nunca redigido
    // e.RedactedErr  -> com os marcadores do registry aplicados
    metrics.Counter("db_error", "error", e.RedactedErr.Error()).Inc()
})

// Liga a redação no hook, no processo inteiro:
inst := goerrshield.New(goerrshield.WithRedactInHook(true))
```

`RedactedErr` fica `nil` enquanto a opção estiver desligada, então um hook que
usa `e.RedactedErr.Error()` precisa checar por `nil` — ou o próprio hook é
instalado numa instância com a opção ligada.

## Setup recomendado

```go
func main() {
	// 1. Log em JSON, para um destino que o cliente não alcance.
	goerrshield.SetLogger(slog.New(slog.NewJSONHandler(os.Stdout, nil)))

	// 2. Métrica para os códigos que o allowlist ainda não cobre.
	goerrshield.SetUnmappedHook(func(e goerrshield.UnmappedEvent) {
		unmappedTotal.WithLabelValues(e.Source, e.Code).Inc()
	})

	// 3. Redigir o que o seu próprio texto de encapsulamento carrega.
	postgres.Registry().Redact("admin@example.com")

	// 4. Nas rotas: c.JSON(safe.HTTPStatus(), safe), e errors.Is para branch.
}
```

Os quatro passos são opcionais e independentes. Sem nenhum deles a lib
funciona — o padrão é logar em `slog.Default()`, sem hook, sem redaction, e o
status sai do seu próprio switch.

```bash
go test -run=XXX -fuzz=FuzzTranslateResponse -fuzztime=30s ./opensearch/...
```

CI em `.github/workflows/ci.yml` roda `gofmt`, `go vet`, `go test -race` e
cobertura em Go 1.22/1.23/1.24.