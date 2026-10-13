FROM golang:1.26-bookworm AS site
WORKDIR /src
COPY go.mod go.sum ./
COPY . .
RUN go run ./cmd/business-brew-site --output /site/business-brew --base-path /business-brew/ --repository-url https://github.com/hoermannpaul/business-brew

FROM mcr.microsoft.com/playwright:v1.64.0-noble
ENV PLAYWRIGHT_SKIP_BROWSER_DOWNLOAD=1
WORKDIR /tests
COPY tests/integration/package.json ./
RUN npm install --ignore-scripts --no-audit --no-fund
COPY tests/integration/ ./
COPY --from=site /site /site
CMD ["npm", "test"]
