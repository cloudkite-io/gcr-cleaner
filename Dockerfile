## We'll choose the incredibly lightweight
## Go alpine image to work with
FROM golang:1.17-buster AS builder

## We create an /assignment directory in which
## we'll put all of our project code

WORKDIR  /go/src/gcr-cleaner
COPY go.* ./
RUN go mod download


COPY . ./


## We want to build our application's binary executable

RUN  CGO_ENABLED=0 GOOS=linux go build -o main cmd/main.go

## the lightweight scratch image we'll
## run our application within
FROM alpine:latest AS production
## We have to copy the output from our
## builder stage to our production stage

RUN apk update && apk add curl python3 py3-pip
RUN curl https://dl.google.com/dl/cloudsdk/release/google-cloud-sdk.tar.gz > /tmp/google-cloud-sdk.tar.gz


# Installing the package
RUN mkdir -p /usr/local/gcloud \
  && tar -C /usr/local/gcloud -xvf /tmp/google-cloud-sdk.tar.gz \
  && /usr/local/gcloud/google-cloud-sdk/install.sh

# Adding the package path to local
ENV PATH $PATH:/usr/local/gcloud/google-cloud-sdk/bin


COPY --from=builder /go/src/gcr-cleaner .

CMD /main