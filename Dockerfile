FROM golang:1.21-alpine AS build
WORKDIR /app
COPY go.mod ./
COPY . .
RUN go build -o /out/faceit-bot .

FROM alpine:3.20
RUN apk add --no-cache ca-certificates tzdata
WORKDIR /app
COPY --from=build /out/faceit-bot /app/faceit-bot
VOLUME ["/app/data"]
CMD ["/app/faceit-bot"]
