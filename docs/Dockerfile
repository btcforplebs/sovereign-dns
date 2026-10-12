# Builds the mdBook docs site and serves it as static files.
FROM rust:1.99.0-slim@sha256:2752b332db73fdbb7dc576f06c82ed1f312005784ef913d7e04a28f5f55dc581 AS build
RUN cargo install mdbook --locked --version =0.4.52
WORKDIR /book
COPY book.toml .
COPY src ./src
RUN mdbook build

FROM nginx:1.30.5-alpine@sha256:0985e772fb9f729e6fa0980da05fca5d9c468e870eed43071545afa9d2e27d94
COPY --from=build /book/book /usr/share/nginx/html
EXPOSE 80
