# vpa-operator

This is a simple operator to watch for new daemonsets, statefulsets, and deployments, and when new ones are created it will add a new VerticalPodAutoscaler.

This currently will ignore any VerticalPodAutoscalers labelled for goldilocks to enable compatibility.
