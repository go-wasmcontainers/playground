// gpu_control.js: a WebGPU render + read-back + ImageBitmap loop with no guest
// (the control of e2e/pane_framerate_test.go): what the quake guest does per frame
// on the GPU, driven from JS at the same size. One frame in flight: the loop waits
// for the read-back, as the guest does, then hands the frame to the page.
//
//   postMessage({ w, h, loops, bitmaps })   start (loops: fragment cost; bitmaps: post each frame)
//   postMessage("stop")                      stop; the worker answers { done, iters, s }
self.onmessage = async (ev) => {
  const { w, h, loops, bitmaps } = ev.data;
  const adapter = await navigator.gpu.requestAdapter();
  const device = await adapter.requestDevice();
  const canvas = new OffscreenCanvas(w, h);
  const ctx = canvas.getContext("webgpu");
  const format = navigator.gpu.getPreferredCanvasFormat();
  ctx.configure({ device, format, alphaMode: "opaque", usage: GPUTextureUsage.RENDER_ATTACHMENT | GPUTextureUsage.COPY_SRC });
  const module = device.createShaderModule({
    code: `
      @vertex fn vs(@builtin(vertex_index) i: u32) -> @builtin(position) vec4f {
        var p = array<vec2f, 3>(vec2f(-1.0, -1.0), vec2f(3.0, -1.0), vec2f(-1.0, 3.0));
        return vec4f(p[i], 0.0, 1.0);
      }
      @fragment fn fs(@builtin(position) p: vec4f) -> @location(0) vec4f {
        var c = 0.0;
        for (var k = 0; k < ${loops}; k++) { c += sin(p.x * 0.01 * f32(k) + p.y * 0.013); }
        return vec4f(fract(c * 0.01), 0.3, 0.5, 1.0);
      }`,
  });
  const pipeline = device.createRenderPipeline({
    layout: "auto",
    vertex: { module, entryPoint: "vs" },
    fragment: { module, entryPoint: "fs", targets: [{ format }] },
    primitive: { topology: "triangle-list" },
  });
  const bpr = Math.ceil((w * 4) / 256) * 256;
  const readback = device.createBuffer({ size: bpr * h, usage: GPUBufferUsage.COPY_DST | GPUBufferUsage.MAP_READ });
  let stop = false;
  self.onmessage = (e) => { if (e.data === "stop") stop = true; };
  let n = 0;
  const t0 = performance.now();
  while (!stop) {
    const tex = ctx.getCurrentTexture();
    const enc = device.createCommandEncoder();
    const pass = enc.beginRenderPass({ colorAttachments: [{ view: tex.createView(), loadOp: "clear", storeOp: "store", clearValue: { r: 0, g: 0, b: 0, a: 1 } }] });
    pass.setPipeline(pipeline);
    pass.draw(3);
    pass.end();
    enc.copyTextureToBuffer({ texture: tex }, { buffer: readback, bytesPerRow: bpr }, [w, h]);
    device.queue.submit([enc.finish()]);
    await readback.mapAsync(GPUMapMode.READ);
    readback.unmap();
    const bm = canvas.transferToImageBitmap();
    if (bitmaps) postMessage(bm, [bm]); else bm.close();
    n++;
  }
  postMessage({ done: true, iters: n, s: (performance.now() - t0) / 1000 });
};
