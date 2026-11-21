FROM golang:1.24.9-alpine3.22 AS build
WORKDIR /src
COPY go.mod go.sum ./
RUN go mod download
COPY api ./api
COPY cmd ./cmd
COPY internal ./internal
RUN CGO_ENABLED=0 go build -trimpath -o /operator ./cmd && CGO_ENABLED=0 go build -trimpath -o /probe ./cmd/probe
FROM scratch
COPY --from=build /operator /operator
COPY --from=build /probe /probe
USER 65532:65532
ENTRYPOINT ["/operator"]
