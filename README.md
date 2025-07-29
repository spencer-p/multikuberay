# Multi KubeRay Dashboard

Multikuberay is a tool that discovers and exposes the Ray dashboard for all
RayClusters you have access to amongst your kubectl contexts. A single web port
is exposed with a left sidebar to navigate between the dashboards seamlessly.

In addition, a runner command is available to automatically set up your
RAY_ADDRESS so you can manage multiple sessions at once.

**Serving:**
```
go install github.com/spencer-p/multikuberay

multikuberay serve
```

Now you have a dashboard at :8080 proxying all of your ray dashboards.

**Runner:** This command will identify the RayCluster you have access to with a
name prefixed with "my-test-" and run the given command with RAY_ADDRESS
pointing at it.

```
multikuberay run my-test- -- ray job submit ...
```

If there are multiple matches, the command will fail and print the matches.
