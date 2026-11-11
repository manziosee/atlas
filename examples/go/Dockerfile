FROM golang:1.27-alpine AS build

WORKDIR /src
COPY go.mod go.sum ./
COPY . .
RUN CGO_ENABLED=0 go build -trimpath -ldflags="-s -w" -o /out/example-go ./examples/go

FROM gcr.io/distroless/static-debian12:nonroot
COPY --from=build /out/example-go /usr/local/bin/example-go
USER 65532:65532
EXPOSE 8085
ENTRYPOINT ["/usr/local/bin/example-go"]
