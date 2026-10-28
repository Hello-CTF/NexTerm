FROM --platform=linux/amd64 golang:1.27.3-bookworm AS go
FROM --platform=linux/amd64 node:24.22.0-bookworm AS node
FROM --platform=linux/amd64 rust:1.95.0-bookworm AS build

COPY --from=go /usr/local/go /usr/local/go
COPY --from=node /usr/local/bin/node /usr/local/bin/node
ENV PATH=/usr/local/go/bin:/usr/local/cargo/bin:$PATH
WORKDIR /src
COPY . .
ARG NEXTERM_RELEASE_VERSION
ARG SOURCE_DATE_EPOCH
RUN node scripts/build.mjs server --release --os=linux --arch=amd64

FROM scratch AS output
COPY --from=build /src/target/go-build/ /
