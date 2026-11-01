# vecnet hub image: static Go binaries on scratch (ADR 0001).
# Build and deploy with `just fly-deploy` (fly deploy --remote-only).

FROM golang:1.26-alpine AS build
RUN apk add --no-cache ca-certificates tzdata
WORKDIR /src
COPY go.mod go.sum ./
RUN go mod download
COPY . .
ENV CGO_ENABLED=0
RUN go build -trimpath -ldflags="-s -w" -o /out/vecnet-hub ./cmd/vecnet-hub \
 && go build -trimpath -ldflags="-s -w" -o /out/vecnet ./cmd/vecnet

FROM scratch
COPY --from=build /etc/ssl/certs/ca-certificates.crt /etc/ssl/certs/ca-certificates.crt
COPY --from=build /usr/share/zoneinfo /usr/share/zoneinfo
COPY --from=build /out/vecnet-hub /vecnet-hub
COPY --from=build /out/vecnet /vecnet
EXPOSE 8080
VOLUME ["/data"]
ENTRYPOINT ["/vecnet-hub","--listen","0.0.0.0:8080","--data-dir","/data"]
