ARG APP_NAME="kube-exec-controller"

FROM golang:1.16 as builder
ARG APP_NAME
WORKDIR /build
COPY . .
RUN GOOS=linux GOARCH=amd64 CGO_ENABLED=0 go build -mod vendor -o ${APP_NAME} cmd/${APP_NAME}/main.go

FROM alpine
ARG APP_NAME
LABEL com.box.name=${APP_NAME}
LABEL maintainer="skynet@box.com"
COPY --from=builder /build/${APP_NAME} /
RUN chmod +x /${APP_NAME}
