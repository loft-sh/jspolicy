# TypeScript Policy

This example holds a typescript policy that denies privileged pods. It can be used as boilerplate for your own typescript policy.

Writing TypeScript policies is as easy as writing them in javascript. You can automatically bundle, test and distribute your policies with TypeScript and npm. jsPolicy provides typings via the `@jspolicy/types` package that you can include in your policy project.

# How to build

After extracting the example, make sure that the dependencies are installed with:
```
npm install
```

After that you can test the policy with:
```
npm run test

> ts-example-policy@0.1.0 test
> jest --env=node --colors --coverage test

 PASS  __tests__/denyPrivilegedPod.ts
  Test denyPrivilegedPod
    ✓ Check container (2 ms)
    ✓ Check init container
    ✓ Check no error if pod is okay (1 ms)

-----------|---------|----------|---------|---------|-------------------
File       | % Stmts | % Branch | % Funcs | % Lines | Uncovered Line #s 
-----------|---------|----------|---------|---------|-------------------
All files  |      80 |       50 |   66.67 |      80 |                   
 policy.ts |      80 |       50 |   66.67 |      80 | 17-18             
-----------|---------|----------|---------|---------|-------------------
Test Suites: 1 passed, 1 total
Tests:       3 passed, 3 total
Snapshots:   0 total
Time:        4.068 s
Ran all test suites matching /test/i.
```

Now compile the policy and generate the kubernetes manifests:
```
npm run compile

> ts-example-policy@0.1.0 compile
> tsc && webpack --config webpack.config.js && node ./scripts/manifests.js

asset bundle.js 857 bytes [compared for emit] [minimized] (name: main)
./lib/index.js 223 bytes [built] [code generated]
./lib/policy.js 726 bytes [built] [code generated]
webpack 5.36.1 compiled successfully in 229 ms
```

Now apply the policy to the cluster:
```
kubectl apply -f manifests/
jspolicy.policy.jspolicy.com/pod-deny-privileged.example.com created
jspolicybundle.policy.jspolicy.com/pod-deny-privileged.example.com created
```

Give jsPolicy a second to create the validating webhook configuration for this policy. You can then check if it was successful via:
```
kubectl apply -f ./example-denied-pod.yaml
Error from server (Forbidden): error when creating "./example-denied-pod.yaml": admission webhook "pod-deny-privileged.example.com" denied the request: spec.containers[0].securityContext.privileged is not allowed
```

