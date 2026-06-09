FROM golang:1.23-alpine AS build
WORKDIR /src
COPY go.mod ./
COPY . .
RUN CGO_ENABLED=0 go build -ldflags "-s -w" -o /branch .

# git is required for save history
FROM alpine:3.20
RUN apk add --no-cache git ca-certificates
COPY --from=build /branch /usr/local/bin/branch
VOLUME /data
EXPOSE 8080
ENTRYPOINT ["branch"]
CMD ["--addr", "0.0.0.0:8080", "/data"]
