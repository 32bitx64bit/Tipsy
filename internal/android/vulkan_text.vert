#version 450

layout(push_constant) uniform FocusedRect {
    vec4 rect;
} pc;

layout(location = 0) out vec2 uv;

void main() {
    vec2 positions[4] = vec2[](
        vec2(pc.rect.x, pc.rect.y),
        vec2(pc.rect.z, pc.rect.y),
        vec2(pc.rect.x, pc.rect.w),
        vec2(pc.rect.z, pc.rect.w)
    );
    vec2 texcoords[4] = vec2[](
        vec2(0.0, 0.0),
        vec2(1.0, 0.0),
        vec2(0.0, 1.0),
        vec2(1.0, 1.0)
    );
    gl_Position = vec4(positions[gl_VertexIndex], 0.0, 1.0);
    uv = texcoords[gl_VertexIndex];
}
