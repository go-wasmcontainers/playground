# mnist: wasi-nn in a container

Reads a handwritten digit with MNIST-12 through wasi-nn
(`wasi_ephemeral_nn`), served in the browser by the engine's pure-Go CPU
backend (`github.com/go-wasmcontainers/gpu/nn`). Copied from
[go-wasmcontainers/gpu](https://github.com/go-wasmcontainers/gpu) v0.2.0,
`examples/mnist`.

```
wctr run --rm mnist            # digit 7 (p=1.000)
wctr run --rm mnist gpu        # fails: no GPU backend in the browser yet
```

- `mnist-12.onnx`: ONNX Model Zoo, `validated/vision/classification/mnist`,
  MIT (model card).
- `digit.png`: picture 0 of the MNIST test set (label 7), Y. LeCun,
  C. Cortes, C. J. C. Burges, CC BY-SA 3.0; extracted unmodified.
