# Builds the mdBook docs site and serves it as static files.
FROM rust:1-slim AS build
RUN cargo install mdbook --locked --version ^0.4
WORKDIR /book
COPY book.toml .
COPY src ./src
RUN mdbook build

FROM nginx:alpine
COPY --from=build /book/book /usr/share/nginx/html
EXPOSE 80
