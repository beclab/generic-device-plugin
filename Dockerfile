FROM golang:1.26.0-alpine AS builder

WORKDIR /src

COPY go.mod go.sum ./
RUN go mod download

COPY . .
RUN CGO_ENABLED=0 go build -o /out/generic-device-plugin .

FROM beclab/alpine:3.18

COPY --from=builder /out/generic-device-plugin /generic-device-plugin

ENTRYPOINT ["/generic-device-plugin"]
