FROM node:22-bookworm AS dash

WORKDIR /src/dash
COPY dash/package.json dash/package-lock.json ./
RUN npm ci
COPY dash/ ./
RUN npm run build

FROM golang:1.25-bookworm AS build

WORKDIR /src
COPY server/go.mod server/go.sum* ./
RUN go mod download
COPY server/ ./
RUN rm -rf internal/app/dash_dist
COPY --from=dash /src/dash/dist ./internal/app/dash_dist

ARG VERSION=dev
ARG SERVER_KEY_ID=
ARG SERVER_PUBLIC_KEY_FINGERPRINT=
RUN CGO_ENABLED=0 GOOS=linux GOARCH=amd64 go build \
    -ldflags "-s -w -X 'ov-computeruse/server/internal/buildinfo.Version=${VERSION}' -X 'ov-computeruse/server/internal/buildinfo.ServerKeyID=${SERVER_KEY_ID}' -X 'ov-computeruse/server/internal/buildinfo.ServerPublicKeyFingerprint=${SERVER_PUBLIC_KEY_FINGERPRINT}'" \
    -o /out/ov-server ./cmd/server

FROM gcr.io/distroless/static-debian12:nonroot
WORKDIR /
COPY --from=build /out/ov-server /ov-server
EXPOSE 8080
USER nonroot:nonroot
ENTRYPOINT ["/ov-server"]

