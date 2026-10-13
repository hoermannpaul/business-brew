FROM golang:1.26-bookworm AS build
WORKDIR /src
COPY go.mod go.sum ./
RUN go mod download
COPY . .
RUN CGO_ENABLED=0 go build -trimpath -o /business-brew-server ./cmd/business-brew-server
RUN mkdir /data

FROM gcr.io/distroless/static-debian12:nonroot
COPY --from=build /business-brew-server /business-brew-server
COPY --from=build --chown=65532:65532 /data /data
WORKDIR /data
VOLUME /data
EXPOSE 8080
ENTRYPOINT ["/business-brew-server"]
