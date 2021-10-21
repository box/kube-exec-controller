# Contributing
All contributions are welcome to this project.

## Contributor License Agreement
Before a contribution can be merged into this project, please fill out the Contributor License Agreement (CLA) located at:

https://cla-assistant.io/box/cla

To learn more about CLAs and why they are important to open source projects, please see the [Wikipedia entry](http://en.wikipedia.org/wiki/Contributor_License_Agreement).

## Code of Conduct
This project adheres to the [Box Open Code of Conduct](http://opensource.box.com/code-of-conduct/). By participating, you are expected to uphold this code.

## How to contribute
* **File an issue** - if you found a bug, want to request an enhancement, or want to implement something (bug fix or feature).
* **Send a pull request** - if you want to contribute code. Please be sure to file an issue first.

## Pull request best practices
We want to accept your pull requests. Please follow these steps:

### Step 1: File an issue
Before writing any code, please file an issue stating the problem you want to solve or the feature you want to implement. This allows us to give you feedback before you spend any time writing code. There may be a known limitation that can't be addressed, or a bug that has already been fixed in a different way. The issue allows us to communicate and figure out if it's worth your time to write a bunch of code for the project.

### Step 2: Fork this repository in GitHub
This will create your own copy of our repository.

### Step 3: Add the upstream source
The upstream source is the project under the Box organization on GitHub. To add an upstream source for this project, type:

```
$ git remote add upstream git@github.com:box/kube-exec-controller.git
```

This will come in useful later.

### Step 4: Create a feature branch
Create a branch with a descriptive name, such as `add-search`.

### Step 5: Push your feature branch to your fork
As you develop code, continue to push code to your remote feature branch. Please make sure to include the issue number you're addressing in your commit message, such as:

```
$ git commit -m "Adding search (fixes #123)"
```

This helps us out by allowing us to track which issue your commit relates to.

Keep a separate feature branch for each issue you want to address.

### Step 6: Rebase
Before sending a pull request, rebase against upstream, such as:

```
$ git fetch upstream
$ git rebase upstream/main
```

This will add your changes on top of what's already in upstream, minimizing merge issues.

### Step 7: Run the tests
Make sure that [all tests are passing](https://app.travis-ci.com/github/box/kube-exec-controller) before submitting a pull request (required for merging any new PRs). You can also run `make test-unit` locally to execute these tests.

Coverage is lacking on the more integration-heavy components, including:
* Receiving admission requests from K8s API-Server
* Evicting Pods with potentially mutated container
* Interacting with commands from kubectl-pi plugin

Until better automated testing of these components is implemented, we expect thorough local testing to ensure that the kube-exec-controller container is still fully functional. You can deploy the app and verify its functionality by:
* Run `make deploy` to apply the app and enable admission control
* Check if the updated kube-exec-controller Pod is up running
* Run `kubectl exec` to a random Pod and verify if the Pod gets evicted
* Run `kubectl pi get/extend` to verify if the commands get handled by kube-exec-controller as expected (see the Install section in [README](README.md) for more detail)

### Step 8: Send the pull request
Send the pull request from your feature branch to us. Be sure to include a description that lets us know what work you did.

Keep in mind that we like to see one issue addressed per pull request, as this helps keep our git history clean and we can more easily track down issues.
