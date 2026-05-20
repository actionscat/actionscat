local lowerMsg = string.lower(msg)

if string.find(lowerMsg, "bilibili%.com") or string.find(lowerMsg, "b23%.tv") then
    return true
else
    return false
end

