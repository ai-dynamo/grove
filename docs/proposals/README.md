# Grove Enhancement Proposal (GREP)

A Grove Enhancement Proposal (GREP) is a structure way to suggest improvements, new features or changes to Grove. It helps you to clearly explain the proposed change(s) conceptually, and to outline the concrete steps needed to reach this goal. It helps the Grove maintainers as well as the community to understand the motivation and scope around your proposed change(s) and encourages their contribution to discussions and future pull requests.

## How to file a GREP

GREPs should be created as Markdown `.md` files and should be submitted for review via Github pull request. Please follow the following rules:
* All GREPs should have an appropriately named directory under `docs/proposals`. The GREP's file name should be `README.md` to be consistent.
* Use the GREP template at `docs/proposals/NNN-template/README.md`
* Ensure that you always generate the table of contents. Use the make target `make update-toc`.
* You can locally verify if the table of contents for your GREP are up-to-date by running `make verify-toc`.

> NOTE: GREP is heavily inspired from Kubernetes KEP template.