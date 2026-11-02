FROM golang:1.26-alpine AS build
WORKDIR /src
COPY go.mod ./
COPY . .
RUN CGO_ENABLED=0 go build -trimpath -ldflags="-s -w" -o /odb ./cmd/odb

FROM alpine:3.22
RUN apk add --no-cache ca-certificates tzdata python3 py3-pip && python3 -m venv /opt/perch-register && adduser -D -u 10001 odb && mkdir /data && chown odb /data
COPY tools/perch-register/requirements.txt /tmp/perch-requirements.txt
RUN /opt/perch-register/bin/pip install --no-cache-dir -r /tmp/perch-requirements.txt
COPY tools/perch-register/ /usr/local/share/perch-register/
RUN chmod -R a+rX /usr/local/share/perch-register
USER odb
COPY --from=build /odb /usr/local/bin/odb
ENV PERCH_REGISTER_PYTHON=/opt/perch-register/bin/python
ENV ODB_ADDR=:8790 ODB_DATA_DIR=/data
EXPOSE 8790
VOLUME ["/data"]
ENTRYPOINT ["odb"]
