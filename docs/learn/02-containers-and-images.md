# 2. Containers and images

*Monday, 13:30.*

"Your fix is merged," Sam says. "What do you think happens next?"

Linh guesses: "Someone copies the new binary onto a server and restarts it?"

"That's how it used to work everywhere. Now we ship something more complete than a binary. Let's look at what CI did with your merge."

## The problem containers solve

A Go binary is not enough to run the backend. It also needs the right CA certificates to talk to the bank's API over HTTPS, a timezone database to format dates, maybe a config file at a known path, and a particular version of the operating system's C library if anything links against it. On a hand-managed server, these pile up over the years, and "works on my machine" becomes "works on server 3 but not server 4".

A **container** solves this by running a program with **its own private filesystem** containing exactly what the program needs, and nothing else from the host. To the program, it looks like it has a small machine of its own. Under the hood it is an ordinary process on the host, isolated using features of the Linux kernel:

- **namespaces** give the process its own view of the filesystem, network, process IDs and hostname,
- **cgroups** limit how much CPU and memory it may use.

A container is not a virtual machine. There is no second operating system kernel; containers share the host's kernel. That is why they start in milliseconds and why you can run dozens on one machine.

## Images: the frozen filesystem

The private filesystem a container starts with comes from an **image**. An image is a packaged, read-only filesystem plus some metadata, most importantly **which command to run** when a container starts from it.

The backend's image is built from a `Dockerfile` in the app's source repo:

```dockerfile
# Build stage: compile the Go binary
FROM golang:1.22 AS build
WORKDIR /src
COPY . .
RUN CGO_ENABLED=0 go build -o /out/backend ./cmd/backend

# Runtime stage: a tiny image with only what the binary needs
FROM gcr.io/distroless/static
COPY --from=build /out/backend /backend
USER nonroot
ENTRYPOINT ["/backend"]
```

Read it top to bottom: start from an image that has the Go toolchain, copy the source in, compile; then start a **fresh**, minimal image (no shell, no package manager, just certificates and timezone data) and copy only the compiled binary into it. The result is a few megabytes and contains almost nothing an attacker could use.

> **Aside: layers.** Each instruction in a Dockerfile produces a **layer**, a set of filesystem changes. An image is a stack of layers. Layers are shared between images and cached, which is why rebuilding after a small code change is fast and why pulling a new version of an image often downloads only one small layer.

## Registries, names and tags

Images are stored in a **registry**, a server that holds images and serves them over HTTPS. Think of it as Git hosting for images. Docker Hub, GitHub Container Registry, and the registries of each cloud provider are all registries. Reconcile uses `registry.example.com`.

An image's full name has three parts:

```
registry.example.com/reconcile-backend:e4f5a6b
└──── registry ────┘ └─ repository ──┘ └ tag ┘
```

- The **repository** is the image's name within the registry. All versions of the backend live in `reconcile-backend`.
- The **tag** is a label for one specific version.

A tag is just a label, like a Git branch name: it points at an image, and someone *could* move it to point at a different image later. The image's true identity is its **digest**, a SHA-256 hash of its contents, like `sha256:9b2c…`. Two images with the same digest are byte-for-byte identical. Same idea as a Git commit hash.

## Which tag to use

Here is what CI did with Linh's merge. The pipeline in the app's source repo:

1. ran the tests,
2. built the image,
3. tagged it with the **first seven characters of the Git commit** that was built, `e4f5a6b`,
4. pushed it to `registry.example.com/reconcile-backend:e4f5a6b`.

Linh checks:

```bash
$ git log --oneline -1
e4f5a6b Fix rounding: sum line items before rounding the total
```

The tag *is* the commit. That gives three useful properties:

- **Traceable.** Given a running tag, you know exactly which source code is running: `git show e4f5a6b`.
- **Unique.** Every build gets a new tag. No two different builds share one.
- **Immutable, if you enforce it.** The registry is configured to refuse pushing a tag that already exists. So `e4f5a6b` will always mean the same image.

Compare that to the tag `latest`, which many tutorials use. `latest` is moved on every build. If the cluster runs `latest`, you cannot tell which code is running, two machines may run different code under the same name, and "rolling back to the previous `latest`" is meaningless.

> **Aside: why immutability matters for security.** If tags can be overwritten, then someone who can push to the registry can replace `e4f5a6b` with different code, and a cluster that re-pulls it will run that code without any change in Git. deployctl's design assumes immutable tags. Deploying by digest instead of tag is on the roadmap for exactly this reason.

## What "deploy" means now

With images, deploying a new version stops being "copy files and restart". It becomes:

> **Tell the place where the app runs to run a different image tag.**

Everything else in this week is about that sentence: *where* the app runs (Kubernetes), how you *tell* it (GitOps), and who is *allowed* to (identity and access).

That also explains what deployctl does **not** do. It does not build images. CI does that, and by the time Linh runs `deployctl deploy`, the image `reconcile-backend:e4f5a6b` already exists in the registry. deployctl only changes which existing tag should run.

"One thing to notice," Sam adds. "deployctl doesn't check that the tag exists in the registry yet. If you typo a tag, the cluster will try to download an image that isn't there and you'll see an error called `ImagePullBackOff`. It's on the roadmap. Remember the name; you'll meet it again on Wednesday."

---

## Check yourself

<details>
<summary>1. What is the difference between a container and an image?</summary>

An image is a packaged, read-only filesystem plus a start command, stored in a registry. A container is a running process started from an image, with that filesystem as its private view of the disk. One image can be used to start many containers.
</details>

<details>
<summary>2. Why does the team tag images with the Git commit SHA instead of <code>latest</code> or <code>v1.2.3</code>?</summary>

The SHA links the running image directly to the exact source code, every build gets a unique tag, and with registry immutability the tag always refers to the same image. `latest` moves and tells you nothing. Version numbers are fine too, but they need a separate step to assign and are easier to reuse by mistake.
</details>

<details>
<summary>3. Two images have tag <code>e4f5a6b</code> but different digests. What happened, and why is it a problem?</summary>

Someone pushed a different image to the same tag, overwriting it. Anything that pulls `e4f5a6b` after that runs different code, while every record (Git, deployctl history) still says `e4f5a6b`. Registry tag immutability prevents this.
</details>

Next: [3. Kubernetes in an afternoon](03-kubernetes.md)
