# GCR CLEANER
GCR Cleaner is a Go tool that deletes stale images in Google Cloud Container Registry or Google Cloud Artifact Registry.
It scans all images in use for a set of kubernetes clusters and delete images not in use that are older than X days. 

## How GCR Cleaner Works
The tool connects to a k8s API server using the current context and gets all in use docker images+tags from all pods. It then compares the GCR images in 
the specified project with the list of docker images+tags in k8s to determine which images are in use. After getting a list of images that are not in use by the
provided cluster(s), it checks if they are older than X days and that they should not be exempted for deletion(based on Image and Tag filters).
The tool then lists all the images marked for deletion and prompts the user before proceeding with deletion.

## Configuration
### Environment variables
| NAME                    | DESCRIPTION                                         | REQUIRED?                     |
| ------------------------ | --------------------------------------------------- | ----------------------------- |
| REGISTRY                | Container registry (gcr.io) or Artifact Registry      | Yes |
| PROJECT_ID              | GCP Project ID where you'd like to delete old images from | Yes |
| AGE_DAYS                | Unused images older than this number of days before current date will be deleted | Yes |
| KUBERNETES_CONTEXTS     | Comma separated Kubernetes Contexts whose images+tags will be used to determine which GCR images are in use. GCR images matching any of these images  will not be deleted | Yes |
| OMIT_IMAGES_REGEX       | Images whose names match this Regex string will not be deleted | No |
| OMIT_TAGS_REGEX         | Images with tags matching this Regex string will not be deleted | No |

#### Example
The environment variables are declared in an .env file
REGISTRY=gcr.io  
PROJECT_ID=project_id  
AGE_DAYS=14  
KUBERNETES_CONTEXTS=ktx_context  
OMIT_IMAGES_REGEX=(image_name|image_sha)  
OMIT_TAGS_REGEX=(latest|prod)  

A sample.env file is provided in the root folder to be used as a template for the environment variables.

### To run using GO
```markdown
    set -o allexport
    source .env
    set +o allexport
```

```markdown
    go run cmd/main.go

```

### To run using docker 

building the image 

```
docker build -t gcr-cleaner:$(date +%m-%d-%Y) .
```

running the image
```
docker run -v ~/.config/gcloud:/root/.config/gcloud -v ~/.kube/config:/root/.kube/config   --env-file .env gcr-cleaner:$(date +%m-%d-%Y)

```
where ~/.kube/config file ```cmd-path``` has been set to the gcloud path in the docker container ``` /usr/local/gcloud/google-cloud-sdk/bin/gcloud```