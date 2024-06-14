FROM --platform=$BUILDPLATFORM golang:latest AS build
ARG TARGETPLATFORM
WORKDIR /build/
COPY . /build/
RUN CGO_ENABLED=0 GOOS=$(echo $TARGETPLATFORM | cut -d'/' -f1) GOARCH=$(echo $TARGETPLATFORM | cut -d'/' -f2) go build -ldflags="-s -w" -o exec
FROM scratch
COPY --from=build /build/exec /bin/shepherd
COPY --from=build /etc/ssl/ /etc/ssl/
ENTRYPOINT ["shepherd"]