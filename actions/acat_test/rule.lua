local lowerMsg = string.lower(msg)

if string.find(lowerMsg, "test") then
    return true
else
    return false
end