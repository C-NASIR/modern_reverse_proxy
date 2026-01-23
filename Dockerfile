FROM golang:1.22 AS build

WORKDIR /src
COPY go.mod go.sum ./
RUN go mod download
COPY . .
RUN CGO_ENABLED=0 GOOS=linux GOARCH=amd64 go build -o /out/proxy ./cmd/proxy

FROM gcr.io/distroless/static-debian12
COPY --from=build /out/proxy /proxy
EXPOSE 8080 9000 9090
ENTRYPOINT ["/proxy"]
