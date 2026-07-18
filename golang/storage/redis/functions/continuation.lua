-- llmtw_continuation_v1
-- Keys: opaque handle index, tenant-hashed record, and (for child writes)
-- operation-key index.
local existing = redis.call('GET', KEYS[1])
if existing then
    local value = redis.call('GET', existing)
    if not value then
        redis.call('DEL', KEYS[1])
    else
        if #KEYS >= 3 then
            local operation = redis.call('GET', KEYS[3])
            if operation then
                return {'existing', operation}
            end
        end
        return {'conflict', ''}
    end
end

if #KEYS >= 3 then
    local operation = redis.call('GET', KEYS[3])
    if operation then
        return {'existing', operation}
    end
end

local ttl = tonumber(ARGV[2])
if ttl and ttl > 0 then
    redis.call('SET', KEYS[2], ARGV[1], 'EX', tostring(ttl), 'NX')
else
    redis.call('SET', KEYS[2], ARGV[1], 'NX')
end
local wrote = redis.call('GET', KEYS[2])
if wrote ~= ARGV[1] then
    return {'conflict', ''}
end
redis.call('SET', KEYS[1], KEYS[2])
if ttl and ttl > 0 then
    redis.call('EXPIRE', KEYS[1], tostring(ttl))
end
if #KEYS >= 3 then
    redis.call('SET', KEYS[3], ARGV[3], 'NX')
    local operation = redis.call('GET', KEYS[3])
    if operation ~= ARGV[3] then
        -- Keep the create-if-absent operation mapping authoritative and
        -- remove this script's provisional record/index on an unexpected
        -- conflict. Redis executes the script atomically, so this is also a
        -- defensive bound against malformed command harnesses.
        redis.call('DEL', KEYS[1], KEYS[2])
        return {'existing', operation}
    end
    if ttl and ttl > 0 then
        redis.call('EXPIRE', KEYS[3], tostring(ttl))
    end
end
return {'created', ''}
